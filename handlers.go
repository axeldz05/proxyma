package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"proxyma/storage"
	"time"
)

func (s *Server) MountHandlers() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.authMiddleware(s.handleUpload))
	mux.HandleFunc("/notify", s.authMiddleware(s.handleNotification))
	mux.HandleFunc("/download/", s.authMiddleware(s.handleDownload))
	mux.HandleFunc("/peers", s.authMiddleware(s.GetPeers))
	mux.HandleFunc("/manifest", s.authMiddleware(s.handleManifest))
	mux.HandleFunc("/file", s.authMiddleware(s.handleDelete))
	mux.HandleFunc("/subscribe", s.authMiddleware(s.handleSubscribe))
	mux.HandleFunc("/services", s.authMiddleware(s.handleListServices))
	mux.HandleFunc("/services/execute", s.authMiddleware(s.handleExecuteService))
	return mux
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.Peers)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	err = s.SaveLocalFile(header.Filename, file)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Bloab uploaded successfully",
	})
}

// handleNotification handles notifications from peers about new files
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var notification PeerNotification
	err := json.NewDecoder(r.Body).Decode(&notification)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if _, subscribed := s.subscriptions.Load(notification.File.Name); subscribed {
		s.downloadQueue <- DownloadJob{
			File:   notification.File,
			Source: notification.Source,
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "Notification received, downloading file")
	} else {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Notification ignored, not subscribed")
	}
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	requestedHash := r.URL.Path[len("/download/"):]
	err := s.storage.ReadBlob(requestedHash, w)
	if err != nil {
		if err == storage.ErrFileDoesNotExist {
			http.Error(w, "Blob not found", http.StatusNotFound)
		} else {
			http.Error(w, "Error retrieving blob: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := map[string]interface{}{
		"status":  "healthy",
		"id":      s.config.ID,
		"files":   len(s.vfs.Snapshot()),
		"peers":   len(s.Peers),
		"address": s.config.Address,
	}

	json.NewEncoder(w).Encode(health)
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientSecret := r.Header.Get("Proxyma-Secret")
		if clientSecret != s.config.Secret {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.config.Services)
}

func (s *Server) handleExecuteService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	serviceName := r.URL.Query().Get("name")
	if serviceName == "" {
        http.Error(w, "Missing 'name' query parameter", http.StatusBadRequest)
		return
    }

    offers := false
    for _, srv := range s.config.Services {
        if srv == serviceName {
            offers = true
            break
        }
    }

    if !offers {
		foundPeer := ""
		for _, peerAddr := range s.getPeersCopy() {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			svcs, err := s.peerClient.DiscoverServices(ctx, peerAddr)
			if err == nil {
				for _, peerSvc := range svcs {
					if peerSvc == serviceName {
						foundPeer = peerAddr
						break
					}
				}
			}
			if foundPeer != "" {
				break
			}
		}

		if foundPeer == "" {
        	http.Error(w, "Service not implemented on this node or anywhere in the cluster", http.StatusNotImplemented)
        	return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := s.peerClient.ExecuteService(ctx, foundPeer, serviceName)
		if err != nil {
			http.Error(w, "Failed to proxy execution", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(result)
		return
    }

    w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"message": fmt.Sprintf("Service %s executed successfully", serviceName),
	})
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fileName := r.URL.Query().Get("name")
	if fileName == "" {
		http.Error(w, "Missing 'name' query parameter", http.StatusBadRequest)
		return
	}
	s.subscriptions.Store(fileName, true)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Subscribed to %s", fileName)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.vfs.Snapshot())
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fileName := r.URL.Query().Get("name")
	if fileName == "" {
		http.Error(w, "Missing 'name' query parameter", http.StatusBadRequest)
		return
	}

	err := s.DeleteLocalFile(fileName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("File deleted successfully"))
}
