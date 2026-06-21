package server

import (
	"crypto/tls"
	"proxyma/internal/p2p"
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
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	return s.serverTLSConfig
}

func (s *Server) ClientTLSConfig() *tls.Config {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	return s.clientTLSConfig
}

func (s *Server) PeerClient() p2p.PeerClient {
	return s.peerClient
}
