package p2p

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequirePeerCN(t *testing.T) {
	t.Run("rejects missing certificate with canonical response", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		cn, ok := RequirePeerCN(rec, req)

		if ok || cn != "" {
			t.Fatalf("RequirePeerCN() = %q, %v; want empty, false", cn, ok)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if got, want := rec.Body.String(), "{\"error\":\"mTLS certificate required\"}\n"; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
	})

	t.Run("returns certificate common name", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{
				Subject: pkix.Name{CommonName: "peer-1"},
			}},
		}

		cn, ok := RequirePeerCN(rec, req)

		if !ok || cn != "peer-1" {
			t.Fatalf("RequirePeerCN() = %q, %v; want peer-1, true", cn, ok)
		}
	})
}
