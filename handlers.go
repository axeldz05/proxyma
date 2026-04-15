package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"proxyma/storage"
)

func (s *Server) MountHandlers() *http.ServeMux {
	mux := http.NewServeMux()
	// --- DOMINIO DE ALMACENAMIENTO (StorageEngine) ---
	mux.HandleFunc("/upload", s.storage.HandleUpload)
	mux.HandleFunc("/download/", s.storage.HandleDownload)
	mux.HandleFunc("/file", s.storage.HandleDelete)
	mux.HandleFunc("/manifest", s.storage.HandleManifest)
	mux.HandleFunc("/subscribe", s.storage.HandleSubscribe)
	mux.HandleFunc("/notify", s.storage.HandleNotification)

	// --- DOMINIO DE CÓMPUTO (ComputeEngine) ---
	mux.HandleFunc("/services/bid", s.compute.HandleServiceBid)
	mux.HandleFunc("/services/submit", s.compute.HandleServiceSubmit)
	mux.HandleFunc("/services/callback", s.compute.HandleServiceCallback)

	// --- DOMINIO DE RED/ESTADO (Aún en el Server por ahora) ---
	mux.HandleFunc("/peers", s.GetPeers)
	return mux
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.Peers)
}

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
		RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	notification, err := DecodeJSON[PeerNotification](r)
	if err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	updated := se.vfs.Upsert(notification.File)
	if updated && !notification.File.Deleted {
		if _, subscribed := se.subscriptions.Load(notification.File.Name); subscribed {
			se.downloadQueue <- DownloadJob{
				File:   notification.File,
				Source: notification.Source,
			}
			RespondJSON(w, http.StatusAccepted, map[string]string{"message": "Downloading file"})
			return
		}
	}
	
	RespondJSON(w, http.StatusOK, map[string]string{"message": "Metadata updated"})
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

func (s *ComputeEngine) HandleServiceBid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var query DiscoveryQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		http.Error(w, "Invalid query payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	schema, exists := s.registry.Get(query.Service)
	if !exists {
		json.NewEncoder(w).Encode(ServiceBid{CanAccept: false})
		return
	}

	for _, reqParam := range query.RequiredParams {
		if _, hasParam := schema.Parameters[reqParam]; !hasParam {
			json.NewEncoder(w).Encode(ServiceBid{CanAccept: false})
			return
		}
	}

	// TODO: El nodo deberia revisar su CPU o su cola interna de tareas en lugar de estimar.
	estimated := int64(100)
	if query.PayloadSizeBytes > 0 {
		mb := query.PayloadSizeBytes / (1024 * 1024)
		estimated += mb * 10
	}
	bid := ServiceBid{
		NodeID:          s.nodeID,
		NodeAddr:        s.nodeAddr,
		Schema:          schema,
		EstimatedMillis: estimated,
		CanAccept:       true,
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(bid)
}

func (s *ComputeEngine) HandleServiceSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var taskReq TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&taskReq); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := s.registry.ValidateRequest(taskReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "Validation failed",
			"details": err.Error(),
		})
		return
	}

	select {
		case s.taskQueue <- taskReq:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "accepted",
				"message": "Task received and queued for processing",
				"job_id":  taskReq.TaskID,
			})
			s.logger.Info("[TaskQueue] - task was queued", "taskID", taskReq.TaskID)
		default:
		    http.Error(w, "Node is overloaded", http.StatusServiceUnavailable)
		    return
	}
}

func (s *ComputeEngine) HandleServiceCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var webhookPayload ServiceTaskResponse
	if err := json.NewDecoder(r.Body).Decode(&webhookPayload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	s.taskStatuses.Store(webhookPayload.TaskID, webhookPayload)
	s.logger.Debug("Webhook received. Task updated", "job_id", webhookPayload.TaskID, "status", webhookPayload.Status)
	w.WriteHeader(http.StatusOK)
}
