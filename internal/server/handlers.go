package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) MountHandlers() http.Handler {
	mux := http.NewServeMux()
	// --- DOMINIO DE ALMACENAMIENTO (StorageEngine) ---
	mux.HandleFunc("/upload", s.Storage.HandleUpload)
	mux.HandleFunc("/download/", s.Storage.HandleDownload)
	mux.HandleFunc("/file", s.Storage.HandleDelete)
	mux.HandleFunc("/manifest", s.Storage.HandleManifest)
	mux.HandleFunc("/subscribe", s.Storage.HandleSubscribe)
	mux.HandleFunc("/notify", s.Storage.HandleNotification)

	// --- DOMINIO DE CÓMPUTO (ComputeEngine) ---
	mux.HandleFunc("/services/bid", s.Compute.HandleServiceBid)
	mux.HandleFunc("/services/submit", s.Compute.HandleServiceSubmit)
	mux.HandleFunc("/services/callback", s.Compute.HandleServiceCallback)
	
	mux.HandleFunc("/peers", s.GetPeers)
	mux.HandleFunc("/sync", s.HandleSync)
	return mux
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	if err := json.NewEncoder(w).Encode(s.peers); err != nil {
		s.Config.Logger.Error("failed to encode getPeers response", "error", err)
	}
}

func (srv *Server) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		PeerIDs []string `json:"peers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	err := srv.ExecuteSync(payload.PeerIDs)

	if err != nil {
		srv.Config.Logger.Error("Sync failed", "error", err)
		http.Error(w, "Sync process encountered errors", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "Storage synced successfully",
	})
}
