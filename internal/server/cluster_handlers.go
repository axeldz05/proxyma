package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"time"
)

type InviteRequest struct {
	ValidForMinutes int `json:"valid_for_minutes"`
}

type InviteResponse struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
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

	caKeyPath := p2p.CAKeyPath(s.Config.CAPath)

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
	smartToken, err := s.LocalInviteGenerate(req.ValidForMinutes)
	if err != nil {
		s.Config.Logger.Error("Failed to generate smart token", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Internal error")
		return
	}

	expiration := time.Now().Add(time.Duration(req.ValidForMinutes) * time.Minute)
	utils.RespondJSON(w, http.StatusCreated, InviteResponse{
		Token:   smartToken,
		Expires: expiration,
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
