package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

func (s *Server) mTLSGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cluster/join" || r.URL.Path == "/relay/forward" || r.URL.Path == "/peers/probe" {
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			s.Config.Logger.Warn("Reject mTLS: tried access without a certificate", "ip", r.RemoteAddr, "path", r.URL.Path)
			utils.RespondError(w, http.StatusForbidden, "mTLS certificate required")
			return
		}
		peerID := r.TLS.PeerCertificates[0].Subject.CommonName
		if peerID != "" {
			if r.URL.Path != "/peers/announce" {
				_, registered := s.Peers.GetPeerRecord(peerID)
				if !registered && peerID != s.Config.ID && peerID != "sponsor" && peerID != "bootstrap" {
					s.Config.Logger.Warn("Reject mTLS: peer not in registry", "peerID", peerID, "ip", r.RemoteAddr)
					utils.RespondError(w, http.StatusForbidden, "peer not registered in cluster")
					return
				}
			}
			s.Peers.SetPeerCertificate(peerID, r.TLS.PeerCertificates[0])
			s.SetPeerOnline(peerID, true)
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
	mux.HandleFunc("GET /services", s.HandleGetServices)
	mux.HandleFunc("POST /schemas/notify", s.HandleSchemaNotify)

	mux.HandleFunc("GET /peers", s.GetPeers)
	mux.HandleFunc("POST /peers/announce", s.HandleAnnounce)
	mux.HandleFunc("POST /peers/add", s.HandleAddPeer)
	mux.HandleFunc("POST /peers/leave", s.HandleLeavePeer)
	mux.HandleFunc("POST /peers/offline", s.HandleOfflinePeer)
	mux.HandleFunc("POST /peers/invite", s.HandleGenerateInvite)
	mux.HandleFunc("POST /peers/probe", s.HandleProbe)
	mux.HandleFunc("POST /cluster/join", s.HandleClusterJoin)
	mux.HandleFunc("POST /cluster/rotate", s.HandleClusterRotate)
	mux.HandleFunc("GET /relay/poll", s.HandleRelayPoll)
	mux.HandleFunc("POST /relay/forward", s.HandleRelayForward)
	mux.HandleFunc("POST /relay/reply", s.HandleRelayReply)
	mux.HandleFunc("GET /telemetry", s.HandleTelemetry)
	mux.HandleFunc("POST /holepunch/init", s.HandleHolePunchInit)
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
	req, ok := utils.DecodeJSONOrError[protocol.AddPeerRequest](w, r)
	if !ok {
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

	sponsorAddr := s.Config.Address
	if isLoopbackOrLocalHost(sponsorAddr) {
		sponsorAddr = "https://" + r.Host
	}

	peersSnapshot := s.GetPeersRecordCopy()
	peersSnapshot[s.Config.ID] = protocol.AddressRecord{
		Addresses: []string{sponsorAddr},
		IsSponsor: s.IsSponsorNode(),
	}

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
	req, ok := utils.DecodeJSONOrError[protocol.JoinRequest](w, r)
	if !ok {
		return
	}

	expiration, exists := s.Invites.CheckAndConsume(req.Secret)

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
	req, ok := utils.DecodeJSONOrError[InviteRequest](w, r)
	if !ok {
		return
	}
	if req.ValidForMinutes <= 0 {
		req.ValidForMinutes = 15
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
	if err != nil {
		s.Config.Logger.Error("Failed to generate smart token", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Internal error")
		return
	}

	expiration := time.Now().Add(time.Duration(req.ValidForMinutes) * time.Minute)
	s.Invites.Add(secretHex, expiration)

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
	req, ok := utils.DecodeJSONOrError[protocol.AddPeerRequest](w, r)
	if !ok {
		return
	}
	s.AddPeer(req.ID, req.Address)
	s.Config.Logger.Info("New peer registered", "peer_id", req.ID, "address", req.Address)
	utils.RespondJSON(w, http.StatusOK, map[string]string{"message": "Peer successfully added"})
}

type PeerIDRequest struct {
	ID string `json:"id"`
}

func (s *Server) HandleLeavePeer(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[PeerIDRequest](w, r)
	if !ok {
		return
	}
	s.RemovePeer(req.ID)
	s.Config.Logger.Info("Peer left cluster", "peer_id", req.ID)
	go s.RotateCAAndResignPeers()
	utils.RespondJSON(w, http.StatusOK, map[string]string{"message": "Peer successfully removed"})
}

func (s *Server) HandleOfflinePeer(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[PeerIDRequest](w, r)
	if !ok {
		return
	}
	s.SetPeerOnline(req.ID, false)
	s.Config.Logger.Info("Peer went offline", "peer_id", req.ID)
	utils.RespondJSON(w, http.StatusOK, map[string]string{"message": "Peer marked as offline"})
}

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.ServiceNotification](w, r)
	if !ok {
		return
	}

	s.Peers.UpdatePeerService(req.NodeID, req.Action, req.Schema)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleSchemaNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.PipelineNotification](w, r)
	if !ok {
		return
	}

	s.Config.Logger.Info("Received pipeline schema notification", "pipelineID", req.Schema.ID, "action", req.Action)

	switch req.Action {
	case "add":
		if s.Storage != nil {
			_ = s.Storage.SavePipelineSchema(req.Schema)
		}
		s.Compute.RegisterPipeline(req.Schema)
	case "remove":
		if s.Storage != nil {
			_ = s.Storage.DeletePipelineSchema(req.Schema.ID)
		}
		s.Compute.UnregisterPipeline(req.Schema.ID)
	}

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

func (s *Server) HandleGetServices(w http.ResponseWriter, r *http.Request) {
	utils.RespondJSON(w, http.StatusOK, s.Compute.ListServices())
}

func (s *Server) HandleProbe(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.ProbeRequest](w, r)
	if !ok {
		return
	}

	if req.Address == "" {
		utils.RespondError(w, http.StatusBadRequest, "Address is required")
		return
	}

	// Security: Only allow probing the caller's IP to prevent SSRF
	callerIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		callerIP = r.RemoteAddr
	}

	// Clean targetHost/Port
	targetAddr := req.Address
	if strings.Contains(targetAddr, "://") {
		parsed, err := url.Parse(targetAddr)
		if err == nil {
			targetAddr = parsed.Host
		}
	}

	_, targetPort, err := net.SplitHostPort(targetAddr)
	if err != nil {
		// If port splitting failed, try treating whole string as host:port
		utils.RespondError(w, http.StatusBadRequest, "Invalid address format, port is required")
		return
	}

	// Always override host with callerIP for safety
	probeAddress := net.JoinHostPort(callerIP, targetPort)
	s.Config.Logger.Debug("Probing reachability for client", "probeAddress", probeAddress)

	conn, err := net.DialTimeout("tcp", probeAddress, 2*time.Second)
	if err != nil {
		utils.RespondJSON(w, http.StatusOK, protocol.ProbeResponse{
			Reachable: false,
			Error:     err.Error(),
		})
		return
	}
	_ = conn.Close()

	utils.RespondJSON(w, http.StatusOK, protocol.ProbeResponse{
		Reachable: true,
	})
}

func (s *Server) HandleClusterRotate(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		utils.RespondError(w, http.StatusForbidden, "mTLS certificate required for CA rotation")
		return
	}
	peerID := r.TLS.PeerCertificates[0].Subject.CommonName
	if peerID == "" {
		utils.RespondError(w, http.StatusForbidden, "Missing peer ID in client certificate")
		return
	}

	record, exists := s.Peers.GetPeerRecord(peerID)
	if !exists || !record.IsSponsor {
		s.Config.Logger.Warn("Reject CA rotation push: sender is not a registered Sponsor/CA authority", "peerID", peerID)
		utils.RespondError(w, http.StatusForbidden, "Only Sponsor/CA authority can rotate cluster certificates")
		return
	}

	req, ok := utils.DecodeJSONOrError[map[string]string](w, r)
	if !ok {
		return
	}

	caCert := req["ca_cert"]
	nodeCert := req["node_cert"]

	if caCert == "" || nodeCert == "" {
		utils.RespondError(w, http.StatusBadRequest, "Missing ca_cert or node_cert")
		return
	}

	certsDir := filepath.Dir(s.Config.CAPath)
	caPath := s.Config.CAPath
	certPath := filepath.Join(certsDir, fmt.Sprintf("%s.crt", s.Config.ID))
	keyPath := filepath.Join(certsDir, fmt.Sprintf("%s.key", s.Config.ID))

	// Fallback to storage path if key is not in certsDir (common in test fixture layouts)
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		testKeyPath := filepath.Join(s.Config.StoragePath, fmt.Sprintf("%s.key", s.Config.ID))
		if _, err := os.Stat(testKeyPath); err == nil {
			keyPath = testKeyPath
			certPath = filepath.Join(s.Config.StoragePath, fmt.Sprintf("%s.crt", s.Config.ID))
		}
	}

	err1 := os.WriteFile(caPath, []byte(caCert), 0644)
	err2 := os.WriteFile(certPath, []byte(nodeCert), 0644)
	if err1 != nil || err2 != nil {
		s.Config.Logger.Error("Failed to save rotated certs", "err1", err1, "err2", err2)
		utils.RespondError(w, http.StatusInternalServerError, "Failed to save rotated certs")
		return
	}

	err := s.ReloadTLSConfig(caPath, certPath, keyPath)
	if err != nil {
		s.Config.Logger.Error("Failed to reload rotated TLS", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Failed to reload rotated TLS: "+err.Error())
		return
	}

	s.Config.Logger.Info("Successfully rotated CA and certificates dynamically.")
	utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "rotated"})
}

func isLoopbackOrLocalHost(addr string) bool {
	parsed, err := url.Parse(addr)
	if err != nil {
		return true
	}
	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || !strings.Contains(host, ".")
}

func (s *Server) HandleHolePunchInit(w http.ResponseWriter, r *http.Request) {
	var msg p2p.HolePunchMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	s.Config.Logger.Info("Received hole punch initialization request", "sender", msg.SenderID, "senderUDP", msg.PublicUDP)

	// Respond with our own public UDP address
	resp := p2p.HolePunchMessage{
		SenderID:  s.Config.ID,
		PublicUDP: s.publicUDPAddr,
	}

	utils.RespondJSON(w, http.StatusOK, resp)

	// Start pinging A in a background goroutine
	if s.quicMgr != nil && msg.PublicUDP != "" {
		rUDPAddr, err := net.ResolveUDPAddr("udp", msg.PublicUDP)
		if err == nil {
			go func() {
				pingPayload := append([]byte{0xff, 0xff, 0xff, 0xff}, []byte("ping:"+s.Config.ID)...)
				// Send 20 pings, 150ms apart
				for i := 0; i < 20; i++ {
					_, _ = s.quicMgr.PacketConn.WriteTo(pingPayload, rUDPAddr)
					time.Sleep(150 * time.Millisecond)
				}
			}()
		}
	}
}

