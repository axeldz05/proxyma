package server

import "time"

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
