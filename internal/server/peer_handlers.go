package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
)

func requirePeerCNMatchesBodyID(w http.ResponseWriter, r *http.Request, bodyID string) bool {
	cn, ok := requirePeerCN(w, r)
	if !ok {
		return false
	}
	if cn != bodyID {
		utils.RespondError(w, http.StatusForbidden, "certificate CN must match peer ID in request body")
		return false
	}
	return true
}

// allowLeaveOrSponsorEvict: self-leave (CN==bodyID) or CA authority evicting another peer.
func (s *Server) allowLeaveOrSponsorEvict(w http.ResponseWriter, r *http.Request, bodyID string) bool {
	cn, ok := requirePeerCN(w, r)
	if !ok {
		return false
	}
	if cn == bodyID {
		return true
	}
	if cn == s.Config.ID && s.Config.CAPath != "" {
		if _, err := os.Stat(p2p.CAKeyPath(s.Config.CAPath)); err == nil {
			return true
		}
	}
	utils.RespondError(w, http.StatusForbidden, "certificate CN must match peer ID, or caller must be CA authority")
	return false
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
	if !requirePeerCNMatchesBodyID(w, r, req.ID) {
		return
	}

	// STUN-like Public IP Detection
	remoteIP := utils.ClientHost(r.RemoteAddr)

	// Try to parse the first announced address to extract scheme and port
	if parsedUrl, err := url.Parse(req.Address.Addresses[0]); err == nil {
		port := parsedUrl.Port()
		if port != "" {
			perceivedAddr := protocol.SchemeAddr(parsedUrl.Scheme, remoteIP, port)

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

	// Never trust self-claimed IsSponsor; sticky only if we already knew them as sponsor
	// (e.g. from a prior trusted announce response from the CA).
	req.Address.IsSponsor = false
	if existing, exists := s.Peers.GetPeerRecord(req.ID); exists {
		req.Address.IsSponsor = existing.IsSponsor
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
		s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) error {
			if peerID == newID {
				return errPeerSkipped
			}
			if err := s.peerClient.AddPeer(peerID, payload); err != nil {
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
	cn, ok := requirePeerCN(w, r)
	if !ok {
		return
	}
	addr := req.Address
	existing, exists := s.Peers.GetPeerRecord(req.ID)
	if cn != req.ID {
		// Gossip fan-out: non-owners must not hijack primary via a higher sequence
		// or elevate IsSponsor (that flag is privilege-bearing for CA rotation).
		if exists && addr.Sequence > existing.Sequence {
			addr.Sequence = existing.Sequence
		}
		addr.IsSponsor = exists && existing.IsSponsor
	} else if exists {
		// Owner re-announce: sticky sponsor only; never accept self-elevation.
		addr.IsSponsor = existing.IsSponsor
	} else {
		addr.IsSponsor = false
	}
	s.AddPeer(req.ID, addr)
	s.Config.Logger.Info("New peer registered", "peer_id", req.ID, "address", addr)
	utils.RespondMessage(w, http.StatusOK, "Peer successfully added")
}

func (s *Server) handlePeerIDAction(w http.ResponseWriter, r *http.Request, logMsg, respMsg string, after func(id string)) {
	req, ok := utils.DecodeJSONOrError[protocol.PeerIDRequest](w, r)
	if !ok {
		return
	}
	if !requirePeerCNMatchesBodyID(w, r, req.ID) {
		return
	}
	after(req.ID)
	s.Config.Logger.Info(logMsg, "peer_id", req.ID)
	utils.RespondMessage(w, http.StatusOK, respMsg)
}

func (s *Server) HandleLeavePeer(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.PeerIDRequest](w, r)
	if !ok {
		return
	}
	if !s.allowLeaveOrSponsorEvict(w, r, req.ID) {
		return
	}
	s.RemovePeer(req.ID)
	s.goOwned(s.RotateCAAndResignPeers)
	s.Config.Logger.Info("Peer left cluster", "peer_id", req.ID)
	utils.RespondMessage(w, http.StatusOK, "Peer successfully removed")
}

func (s *Server) HandleOfflinePeer(w http.ResponseWriter, r *http.Request) {
	s.handlePeerIDAction(w, r, "Peer went offline", "Peer marked as offline", func(id string) {
		s.SetPeerOffline(id, nil)
	})
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
	callerIP := utils.ClientHost(r.RemoteAddr)

	// Clean targetHost/Port
	targetAddr := utils.StripURLScheme(req.Address)
	if strings.Contains(req.Address, "://") {
		parsed, err := url.Parse(req.Address)
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

	conn, err := net.DialTimeout("tcp", probeAddress, PeerRPCProbe)
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
