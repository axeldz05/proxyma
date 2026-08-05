package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

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
	if utils.IsLoopbackHost(sponsorAddr) {
		sponsorAddr = "https://" + r.Host
	}

	peersSnapshot := s.GetPeersRecordCopy()
	peersSnapshot[s.Config.ID] = protocol.AddressRecord{
		Addresses: []string{sponsorAddr},
		IsSponsor: s.IsSponsorNode(),
	}

	go func(newID string, newAddress protocol.AddressRecord) {
		payload := protocol.AddPeerRequest{ID: newID, Address: newAddress}
		bodyBytes, _ := json.Marshal(payload)
		s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) error {
			if peerID == newID {
				return nil
			}
			if err := s.peerClient.AddPeer(peerID, bytes.NewBuffer(bodyBytes)); err != nil {
				s.Config.Logger.Warn("couldn't request to add new peer", "target-peer", peerID, "newPeer", newAddress, "error", err)
				return err
			}
			return nil
		})
	}(req.ID, req.Address)

	utils.RespondJSON(w, http.StatusOK, peersSnapshot)
}
func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.GetPeersRecordCopy()
	utils.RespondJSON(w, http.StatusOK, peers)
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
