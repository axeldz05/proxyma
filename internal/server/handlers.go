package server

import (
	"maps"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"strings"
	"time"
)

func (s *Server) mTLSGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cluster/join" {
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			s.Config.Logger.Warn("Reject mTLS: tried access without a certificate", "ip", r.RemoteAddr, "path", r.URL.Path)
			http.Error(w, "mTLS certificate required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
	mux.HandleFunc("/peers/announce", s.HandleAnnounce)
	mux.HandleFunc("/peers/add", s.HandleAddPeer)
	mux.HandleFunc("/peers/invite", s.HandleGenerateInvite)
	mux.HandleFunc("/sync", s.HandleSync)
	mux.HandleFunc("/cluster/join", s.HandleClusterJoin)
	return s.mTLSGuard(mux)
}

type InviteRequest struct {
	ValidForMinutes int `json:"valid_for_minutes"`
}

type InviteResponse struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

func (s *Server) HandleAnnounce(w http.ResponseWriter, r *http.Request) {
	var req protocol.AddPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	s.AddPeer(req.ID, req.Address)
	
	peersSnapshot := make(map[string]string)
	s.inviteMu.Lock()
	maps.Copy(peersSnapshot, s.peers)
	s.inviteMu.Unlock()
	peersSnapshot[s.Config.ID] = s.Config.Address

	go func(newID, newAddress string, clusterPeers map[string]string) {
		payload := protocol.AddPeerRequest{ ID: newID, Address: newAddress }
		bodyBytes, _ := json.Marshal(payload)
		for peerID, peerAddress := range clusterPeers {
			if peerID == s.Config.ID || peerID == newID { continue }
			if err := s.peerClient.AddPeer(peerAddress, bytes.NewBuffer(bodyBytes)); err != nil {
				s.Config.Logger.Warn("couldn't request to add new peer", "target-peer", peerAddress, "newPeer", req.Address, "error", err)
			}
		}
	}(req.ID, req.Address, peersSnapshot)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(peersSnapshot)
}

func (s *Server) HandleClusterJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req protocol.JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	s.inviteMu.Lock()
	expiration, exists := s.pendingInvites[req.Secret]
	if exists {
		// Valid or not, after one use it should be deleted from memory
		delete(s.pendingInvites, req.Secret)
	}
	s.inviteMu.Unlock()

	if !exists {
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}

	if time.Now().After(expiration) {
		http.Error(w, "Token has expired", http.StatusUnauthorized)
		return
	}

	caKeyPath := strings.Replace(s.Config.CAPath, ".crt", ".key", 1)
	
	newCertPEM, err := p2p.SignCSR([]byte(req.CSR), s.Config.CAPath, caKeyPath)
	if err != nil {
		s.Config.Logger.Error("Error signing CSR", "error", err)
		http.Error(w, "Failed to generate certificate", http.StatusInternalServerError)
		return
	}

	caCertPEM, err := os.ReadFile(s.Config.CAPath)
	if err != nil {
		http.Error(w, "Internal error reading CA", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(protocol.JoinResponse{
		Certificate: string(newCertPEM),
		CACert:      string(caCertPEM),
	})
	
	s.Config.Logger.Info("New node successfully joined the cluster via invitation")
}

func (s *Server) HandleGenerateInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req InviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ValidForMinutes <= 0 {
		req.ValidForMinutes = 15 
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath)
	if err != nil {
		s.Config.Logger.Error("Failed to generate smart token", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	expiration := time.Now().Add(time.Duration(req.ValidForMinutes) * time.Minute)
	s.inviteMu.Lock()
	s.pendingInvites[secretHex] = expiration
	s.inviteMu.Unlock()

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(InviteResponse{
		Token:   smartToken,
		Expires: expiration,
	})
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	if err := json.NewEncoder(w).Encode(s.peers); err != nil {
		s.Config.Logger.Error("failed to encode getPeers response", "error", err)
	}
}

func (s *Server) HandleAddPeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req protocol.AddPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.Config.Logger.Error("Invalid body petition in /peers/add", "error", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return 
	}
	s.AddPeer(req.ID, req.Address)
	s.Config.Logger.Info("New peer registered", "peer_id", req.ID, "address", req.Address)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message": "Peer successfully added"}`))
}

func (srv *Server) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	err := srv.ExecuteSync()
	if err != nil {
		srv.Config.Logger.Error("Sync failed", "error", err)
		http.Error(w, "Sync process encountered errors", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message": "Sync successfully processed"}`))
}
