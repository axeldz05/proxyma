package storage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/storage/physical"
	"proxyma/internal/utils"
)

func (s *StorageEngine) HandleUpload(w http.ResponseWriter, r *http.Request) {
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
func (se *StorageEngine) HandleNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	notification, err := utils.DecodeJSON[p2p.PeerNotification](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	updated := se.vfs.Upsert(notification.File)
	if updated && !notification.File.Deleted {
		if _, subscribed := se.subscriptions.Load(notification.File.Name); subscribed {
			se.downloadQueue <- DownloadJob{
				File:   notification.File,
				Source: notification.Source,
			}
			utils.RespondJSON(w, http.StatusAccepted, map[string]string{"message": "Downloading file"})
			return
		}
	}
	
	utils.RespondJSON(w, http.StatusOK, map[string]string{"message": "Metadata updated"})
}

func (s *StorageEngine) HandleDownload(w http.ResponseWriter, r *http.Request) {
	requestedHash := r.URL.Path[len("/download/"):]
	err := s.physical.ReadBlob(requestedHash, w)
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

func (s *StorageEngine) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
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
	s.logger.Info("Subscription added", "file", fileName)
	fmt.Fprintf(w, "Subscribed to %s", fileName)
}

func (s *StorageEngine) HandleManifest(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.vfs.Snapshot())
}

func (s *StorageEngine) HandleDelete(w http.ResponseWriter, r *http.Request) {
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
