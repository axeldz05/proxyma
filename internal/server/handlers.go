package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
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
			utils.RespondError(w, http.StatusForbidden, "mTLS certificate required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) MountHandlers() http.Handler {
	mux := http.NewServeMux()
	// --- DOMINIO DE ALMACENAMIENTO (StorageEngine) ---
	mux.HandleFunc("POST /upload", s.Storage.HandleUpload)
	mux.HandleFunc("GET /download/", s.Storage.HandleDownload)
	mux.HandleFunc("DELETE /file", s.Storage.HandleDelete)
	mux.HandleFunc("GET /manifest", s.Storage.HandleManifest)
	mux.HandleFunc("POST /subscribe", s.Storage.HandleSubscribe)
	mux.HandleFunc("POST /notify", s.Storage.HandleNotification)

	mux.HandleFunc("POST /services/bid", s.Compute.HandleServiceBid)
	mux.HandleFunc("POST /services/submit", s.Compute.HandleServiceSubmit)
	mux.HandleFunc("POST /services/callback", s.Compute.HandleServiceCallback)
	mux.HandleFunc("POST /services/notify", s.HandleServiceNotify)

	mux.HandleFunc("GET /peers", s.GetPeers)
	mux.HandleFunc("POST /peers/announce", s.HandleAnnounce)
	mux.HandleFunc("POST /peers/add", s.HandleAddPeer)
	mux.HandleFunc("POST /peers/invite", s.HandleGenerateInvite)
	mux.HandleFunc("POST /cluster/join", s.HandleClusterJoin)
	mux.HandleFunc("GET /relay/poll", s.HandleRelayPoll)
	mux.HandleFunc("POST /relay/forward", s.HandleRelayForward)
	mux.HandleFunc("POST /relay/reply", s.HandleRelayReply)
	mux.HandleFunc("GET /telemetry", s.HandleTelemetry)
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
	req, err := utils.DecodeJSON[protocol.AddPeerRequest](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.ID == "" || len(req.Address.Addresses) == 0 || req.Address.Addresses[0] == "" {
		utils.RespondError(w, http.StatusBadRequest, "ID and Address cannot be empty")
		return
	}

	// STUN-like Public IP Detection
	remoteIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remoteIP = host
	}
	
	// Try to parse the first announced address to extract scheme and port
	if parsedUrl, err := url.Parse(req.Address.Addresses[0]); err == nil {
		port := parsedUrl.Port()
		if port != "" {
			perceivedAddr := parsedUrl.Scheme + "://" + remoteIP + ":" + port
			
			// Add perceivedAddr if it's not already in the list
			exists := false
			for _, addr := range req.Address.Addresses {
				if addr == perceivedAddr {
					exists = true
					break
				}
			}
			if !exists {
				req.Address.Addresses = append(req.Address.Addresses, perceivedAddr)
			}
		}
	}

	s.AddPeer(req.ID, req.Address)

	peersSnapshot := s.GetPeersRecordCopy()
	peersSnapshot[s.Config.ID] = protocol.AddressRecord{Addresses: []string{s.Config.Address}}

	go func(newID string, newAddress protocol.AddressRecord, clusterPeers map[string]protocol.AddressRecord) {
		payload := protocol.AddPeerRequest{ID: newID, Address: newAddress}
		bodyBytes, _ := json.Marshal(payload)
		for peerID, peerRecord := range clusterPeers {
			if peerID == s.Config.ID || peerID == newID || len(peerRecord.Addresses) == 0 {
				continue
			}
			peerAddress := peerRecord.Addresses[0]
			if err := s.peerClient.AddPeer(peerAddress, bytes.NewBuffer(bodyBytes)); err != nil {
				s.Config.Logger.Warn("couldn't request to add new peer", "target-peer", peerAddress, "newPeer", req.Address, "error", err)
			}
		}
	}(req.ID, req.Address, peersSnapshot)

	utils.RespondJSON(w, http.StatusOK, peersSnapshot)
}

func (s *Server) HandleClusterJoin(w http.ResponseWriter, r *http.Request) {
	req, err := utils.DecodeJSON[protocol.JoinRequest](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
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
		utils.RespondError(w, http.StatusUnauthorized, "Invalid or expired token")
		return
	}

	if time.Now().After(expiration) {
		utils.RespondError(w, http.StatusUnauthorized, "Token has expired")
		return
	}

	if req.Address == "" {
		utils.RespondError(w, http.StatusBadRequest, "Address is required")
		return
	}
	parsedUrl, err := url.Parse(req.Address)
	if err != nil || parsedUrl.Host == "" {
		utils.RespondError(w, http.StatusBadRequest, "Invalid address format")
		return
	}
	// Note: We cannot perform a net.DialTimeout here because the joining node
	// is currently running 'proxyma join' and its HTTP server hasn't started yet.

	caKeyPath := strings.Replace(s.Config.CAPath, ".crt", ".key", 1)

	newCertPEM, err := p2p.SignCSR([]byte(req.CSR), s.Config.CAPath, caKeyPath)
	if err != nil {
		s.Config.Logger.Error("Error signing CSR", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Failed to generate certificate")
		return
	}

	caCertPEM, err := os.ReadFile(s.Config.CAPath)
	if err != nil {
		utils.RespondError(w, http.StatusInternalServerError, "Internal error reading CA")
		return
	}

	utils.RespondJSON(w, http.StatusOK, protocol.JoinResponse{
		Certificate: string(newCertPEM),
		CACert:      string(caCertPEM),
	})

	s.Config.Logger.Info("New node successfully joined the cluster via invitation")
}

func (s *Server) HandleGenerateInvite(w http.ResponseWriter, r *http.Request) {
	req, err := utils.DecodeJSON[InviteRequest](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.ValidForMinutes <= 0 {
		req.ValidForMinutes = 15
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath)
	if err != nil {
		s.Config.Logger.Error("Failed to generate smart token", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Internal error")
		return
	}

	expiration := time.Now().Add(time.Duration(req.ValidForMinutes) * time.Minute)
	s.inviteMu.Lock()
	s.pendingInvites[secretHex] = expiration
	s.inviteMu.Unlock()

	utils.RespondJSON(w, http.StatusCreated, InviteResponse{
		Token:   smartToken,
		Expires: expiration,
	})
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.GetPeersRecordCopy()
	if err := json.NewEncoder(w).Encode(peers); err != nil {
		s.Config.Logger.Error("failed to encode getPeers response", "error", err)
	}
}

func (s *Server) HandleAddPeer(w http.ResponseWriter, r *http.Request) {
	req, err := utils.DecodeJSON[protocol.AddPeerRequest](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	s.AddPeer(req.ID, req.Address)
	s.Config.Logger.Info("New peer registered", "peer_id", req.ID, "address", req.Address)
	utils.RespondJSON(w, http.StatusOK, map[string]string{"message": "Peer successfully added"})
}

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	req, err := utils.DecodeJSON[protocol.ServiceNotification](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	s.clusterServicesMu.Lock()
	if s.clusterServices[req.NodeID] == nil {
		s.clusterServices[req.NodeID] = make(map[string]protocol.ServiceSchema)
	}
	switch req.Action {
	case "add", "modify":
		s.clusterServices[req.NodeID][req.Schema.Name] = req.Schema
		s.Config.Logger.Info("Cluster service registered", "service", req.Schema.Name, "peer", req.NodeID)
	case "remove":
		delete(s.clusterServices[req.NodeID], req.Schema.Name)
		s.Config.Logger.Info("Cluster service removed", "service", req.Schema.Name, "peer", req.NodeID)
	}
	s.clusterServicesMu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleTelemetry(w http.ResponseWriter, r *http.Request) {
	memLimit := utils.ReadMemoryLimit()
	cpuLimit := utils.ReadCPULimit()

	res := map[string]any{
		"node_id":      s.Config.ID,
		"cpu_limit":    cpuLimit,
		"memory_limit": memLimit,
	}

	w.Header().Set("Content-Type", "application/json")
	utils.RespondJSON(w, http.StatusOK, res)
}
