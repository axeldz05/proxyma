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
	return mux
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.peers)
}
