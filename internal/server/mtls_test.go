package server

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyma/internal/protocol"
)

func TestMTLSGuardRequiresReservedCNsToBeRegistered(t *testing.T) {
	t.Parallel()

	for _, peerID := range []string{"sponsor", "bootstrap"} {
		t.Run(peerID, func(t *testing.T) {
			s := newMTLSGuardTestServer("self")
			status := serveGuardedRequest(s, protocol.PathPeers, peerID)
			if status != http.StatusForbidden {
				t.Fatalf("unregistered CN %q status = %d, want %d", peerID, status, http.StatusForbidden)
			}
		})
	}
}

func TestMTLSGuardAllowsRegisteredPeerSelfAndAnnounce(t *testing.T) {
	t.Parallel()

	t.Run("registered reserved CN", func(t *testing.T) {
		s := newMTLSGuardTestServer("self")
		_, _ = s.Peers.AddPeer("sponsor", protocol.AddressRecord{
			Addresses: []string{"https://sponsor.invalid"},
		})
		if status := serveGuardedRequest(s, protocol.PathPeers, "sponsor"); status != http.StatusNoContent {
			t.Fatalf("registered peer status = %d, want %d", status, http.StatusNoContent)
		}
	})

	t.Run("self", func(t *testing.T) {
		s := newMTLSGuardTestServer("self")
		if status := serveGuardedRequest(s, protocol.PathPeers, "self"); status != http.StatusNoContent {
			t.Fatalf("self status = %d, want %d", status, http.StatusNoContent)
		}
	})

	t.Run("unregistered announce", func(t *testing.T) {
		s := newMTLSGuardTestServer("self")
		if status := serveGuardedRequest(s, protocol.PathPeersAnnounce, "new-peer"); status != http.StatusNoContent {
			t.Fatalf("announce status = %d, want %d", status, http.StatusNoContent)
		}
	})
}

func newMTLSGuardTestServer(selfID string) *Server {
	logger := slog.Default()
	return &Server{
		Config: protocol.NodeConfig{ID: selfID, Logger: logger},
		Peers:  NewPeerRegistry(logger, selfID),
	}
}

func serveGuardedRequest(s *Server, path, peerID string) int {
	handler := s.mTLSGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: peerID},
		}},
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}
