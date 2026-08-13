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

func forbidMissingMTLS(w http.ResponseWriter) {
	p2p.ForbidMissingMTLS(w)
}

// requirePeerCN extracts the peer CN or writes the standard missing-mTLS 403.
func requirePeerCN(w http.ResponseWriter, r *http.Request) (cn string, ok bool) {
	return p2p.RequirePeerCN(w, r)
}

func (s *Server) mTLSGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := s.routeAuth(r.URL.Path)
		if mode == authAnonymous {
			next.ServeHTTP(w, r)
			return
		}
		peerID, ok := peerCNFromRequest(r)
		if !ok {
			s.Config.Logger.Warn("Reject mTLS: tried access without a certificate", "ip", r.RemoteAddr, "path", r.URL.Path)
			forbidMissingMTLS(w)
			return
		}
		if mode != authMTLSUnregistered {
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
