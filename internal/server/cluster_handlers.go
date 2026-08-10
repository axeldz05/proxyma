package server

import (
	"net/http"
	"net/url"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *Server) HandleClusterJoin(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.JoinRequest](w, r)
	if !ok {
		return
	}

	if _, exists := s.Invites.CheckAndConsume(req.Secret); !exists {
		utils.RespondError(w, http.StatusUnauthorized, "Invalid or expired token")
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

	if req.ID == "" {
		utils.RespondError(w, http.StatusBadRequest, "ID is required")
		return
	}
	csrCN, err := p2p.CSRCommonName([]byte(req.CSR))
	if err != nil {
		s.Config.Logger.Error("Error parsing CSR", "error", err)
		utils.RespondError(w, http.StatusBadRequest, "Invalid CSR")
		return
	}
	if csrCN != req.ID {
		utils.RespondError(w, http.StatusBadRequest, "CSR CommonName must match join ID")
		return
	}

	caKeyPath := p2p.CAKeyPath(s.Config.CAPath)

	newCertPEM, err := p2p.SignCSR([]byte(req.CSR), s.Config.CAPath, caKeyPath)
	if err != nil {
		s.Config.Logger.Error("Error signing CSR", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Failed to generate certificate")
		return
	}

	caCertPEM, err := p2p.ReadCAPEM(s.Config.CAPath)
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
	req, ok := utils.DecodeJSONOrError[protocol.InviteRequest](w, r)
	if !ok {
		return
	}
	if req.ValidForMinutes <= 0 {
		req.ValidForMinutes = protocol.DefaultInviteMinutes
	}
	smartToken, expiration, err := s.LocalInviteGenerate(req.ValidForMinutes)
	if err != nil {
		s.Config.Logger.Error("Failed to generate smart token", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Internal error")
		return
	}

	utils.RespondJSON(w, http.StatusCreated, protocol.InviteResponse{
		Token:   smartToken,
		Expires: expiration,
	})
}

func (s *Server) HandleClusterRotate(w http.ResponseWriter, r *http.Request) {
	peerID, ok := peerCNFromRequest(r)
	if !ok {
		utils.RespondError(w, http.StatusForbidden, "mTLS certificate required for CA rotation")
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

	caPath := s.Config.CAPath
	certPath, keyPath := p2p.ResolveNodeCertPaths(caPath, s.Config.StoragePath, s.Config.ID)

	if err := p2p.WriteNodePEMs(caPath, certPath, "", []byte(caCert), []byte(nodeCert), nil); err != nil {
		s.Config.Logger.Error("Failed to save rotated certs", "error", err)
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
