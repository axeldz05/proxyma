package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"proxyma/storage"
)

func (s *Server) MountHandlers() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/notify", s.handleNotification)
	mux.HandleFunc("/download/", s.handleDownload)
	mux.HandleFunc("/peers", s.GetPeers)
	mux.HandleFunc("/manifest", s.handleManifest)
	mux.HandleFunc("/file", s.handleDelete)
	mux.HandleFunc("/subscribe", s.handleSubscribe)
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
	updated := s.vfs.Upsert(notification.File)
	if updated && !notification.File.Deleted {
		if _, subscribed := s.subscriptions.Load(notification.File.Name); subscribed {
			s.downloadQueue <- DownloadJob{
				File:   notification.File,
				Source: notification.Source,
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, "Notification received, downloading file")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Metadata updated, blob ignored (not subscribed or deleted)")
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
	s.config.Logger.Info("Subscription added", "file", fileName)
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
