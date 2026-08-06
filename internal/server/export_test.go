package server

import (
	"context"
	"crypto/tls"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"time"
)

// SetPendingInviteExpiration exposes pendingInvites manipulation for external test packages.
// This file is only compiled during `go test`.
func (s *Server) SetPendingInviteExpiration(secret string, t time.Time) {
	s.Invites.Add(secret, t)
}

func (s *Server) GetHTTPServerAddr() string {
	if s.httpServer == nil {
		return ""
	}
	return s.httpServer.Addr
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
