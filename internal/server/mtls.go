package server

import (
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/utils"
)

// peerCNFromRequest extracts the peer CommonName from the request TLS state (L2).
func peerCNFromRequest(r *http.Request) (string, bool) {
	return p2p.PeerCNFromTLS(r.TLS)
}

func (s *Server) mTLSGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cluster/join" || r.URL.Path == "/relay/forward" || r.URL.Path == "/peers/probe" {
			next.ServeHTTP(w, r)
			return
		}
		peerID, ok := peerCNFromRequest(r)
		if !ok {
			s.Config.Logger.Warn("Reject mTLS: tried access without a certificate", "ip", r.RemoteAddr, "path", r.URL.Path)
			utils.RespondError(w, http.StatusForbidden, "mTLS certificate required")
			return
		}
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
		next.ServeHTTP(w, r)
	})
}
