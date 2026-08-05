package server

import (
	"net/http"
	"proxyma/internal/utils"
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
