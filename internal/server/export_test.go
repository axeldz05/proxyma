package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"time"
)

// SetPendingInviteExpiration exposes pendingInvites manipulation for external test packages.
// This file is only compiled during `go test`.
func (s *Server) SetPendingInviteExpiration(secret string, t time.Time) {
	s.Invites.Add(secret, t)
}

func (s *Server) ServerTLSConfig() *tls.Config {
	s.tlsMutex.RLock()
	defer s.tlsMutex.RUnlock()
	return s.serverTLSConfig
}

func (s *Server) ClientTLSConfig() *tls.Config {
	s.tlsMutex.RLock()
	defer s.tlsMutex.RUnlock()
	return s.clientTLSConfig
}

func (s *Server) PeerClient() p2p.PeerClient {
	return s.peerClient
}

// FetchBlobFromPeer exposes fetchBlobFromPeer for functional tests.
func (s *Server) FetchBlobFromPeer(ctx context.Context, peerID string, entry protocol.IndexEntry) error {
	return s.fetchBlobFromPeer(ctx, peerID, entry)
}

// AttachQUICManager mounts a QUICManager for tests that skip CheckNAT (e.g. IsSponsorOverride).
func (s *Server) AttachQUICManager(qm *p2p.QUICManager) {
	s.quicMgr = qm
	if qm != nil {
		s.publicUDPAddr = qm.PublicUDPAddr
		s.peerClient.SetQUICManager(qm)
	}
}

// QUICManager returns the active QUIC manager (may be nil).
func (s *Server) QUICManager() *p2p.QUICManager {
	return s.quicMgr
}

// PublicUDPAddr returns the advertised public UDP address.
func (s *Server) PublicUDPAddr() string {
	return s.publicUDPAddr
}

// ProcessRelayRequest exposes processRelayRequest for tests.
func (s *Server) ProcessRelayRequest(sponsorAddr string, relayReq protocol.RelayRequest) {
	s.processRelayRequest(sponsorAddr, relayReq)
}

// RefreshPublicUDPFromMapping exposes refreshPublicUDPFromMapping for tests.
func (s *Server) RefreshPublicUDPFromMapping(extIP string, mappedUDP int) {
	s.refreshPublicUDPFromMapping(extIP, mappedUDP)
}

// SetPublicUDPAddr sets publicUDPAddr for tests.
func (s *Server) SetPublicUDPAddr(addr string) {
	s.publicUDPAddr = addr
	if s.quicMgr != nil {
		s.quicMgr.PublicUDPAddr = addr
	}
}

// SetHTTPHandler replaces the in-process mux used by processRelayRequest (tests only).
func (s *Server) SetHTTPHandler(h http.Handler) {
	s.handler = h
}

// SelectBestServiceBid exposes selectBestServiceBid for external test packages.
func SelectBestServiceBid(bids []protocol.ServiceBid, strategy string) protocol.ServiceBid {
	return selectBestServiceBid(bids, strategy)
}
