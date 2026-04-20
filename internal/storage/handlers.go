package storage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"proxyma/internal/protocol"
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
	defer func() { _ = file.Close()}()

	err = s.SaveLocalFile(header.Filename, file)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if err = file.Close(); err != nil {
		http.Error(w, "Couldn't close file", http.StatusInternalServerError)
	}

	w.WriteHeader(http.StatusCreated)
	if err = json.NewEncoder(w).Encode(map[string]string{
		"message": "Blob uploaded successfully",
	}); err != nil {
		s.logger.Error("failed to encode upload response", "error", err)
	}
}


func (s *StorageEngine) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peers, err := utils.DecodeJSON[map[string]string](r)
	if err != nil {
		http.Error(w, "couldn't decode peers json", http.StatusInternalServerError)
		s.logger.Error("failed to decode peers json", "error", err)
		return
	}
	// TODO: make it return a multi-status if some peers failed to sync, or a 
	// 400 error if everyone failed
	err = s.SyncStorage(peers)
	if err != nil {
		http.Error(w, "couldn't sync storage", http.StatusInternalServerError)
		s.logger.Error("failed to sync storage", "error", err, "peers", peers)
		return
	}

	w.WriteHeader(http.StatusOK)
	if err = json.NewEncoder(w).Encode(map[string]string{
		"message": "Storage synced succesfully",
	}); err != nil {
		s.logger.Error("failed to encode storage sync response", "error", err)
	}
}

// handleNotification handles notifications from peers about new files
func (se *StorageEngine) HandleNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	notification, err := utils.DecodeJSON[protocol.PeerNotification](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	updated := se.vfs.Upsert(notification.File)
	if updated && !notification.File.Deleted {
		if se.isSubscribed(notification.File.Name) {
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
	w.Header().Set("Content-Type", "application/octet-stream")
	err := s.physical.ReadBlob(requestedHash, w)
	if err != nil {
        if err == storage.ErrFileDoesNotExist {
            http.Error(w, "Blob not found", http.StatusNotFound)
        }
        return
    }
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
	s.SetSubscription(fileName, true)
	w.WriteHeader(http.StatusOK)
	s.logger.Info("Subscription added", "file", fileName)
	if _, err := fmt.Fprintf(w, "Subscribed to %s", fileName); err != nil {
		http.Error(w, "Couldn't write message", http.StatusInternalServerError)
		return
	}
}

func (s *StorageEngine) HandleManifest(w http.ResponseWriter, r *http.Request) {
	if err := json.NewEncoder(w).Encode(s.vfs.Snapshot()); err != nil {
		s.logger.Error("failed to encode snapshot response", "error", err)
	}
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
	_, _ = w.Write([]byte("File deleted successfully"))
}
