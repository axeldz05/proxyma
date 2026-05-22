package p2p_test

import (
	"net/http"
	"net/http/httptest"
	"proxyma/internal/p2p"
	"testing"
)

// newMockServer creates a TLS httptest server with the given handler and returns
// an HTTPPeerClient configured to talk to it. The server is closed on test cleanup.
func newMockServer(t *testing.T, handler http.Handler) (string, *p2p.HTTPPeerClient) {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)

	client := p2p.NewHTTPPeerClient(ts.Client().Transport, "", nil)
	return ts.URL, client
}
