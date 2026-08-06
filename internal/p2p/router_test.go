package p2p_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestP2PRoundTripperDirectRouting(t *testing.T) {
	t.Parallel()

	// 1. Setup a real HTTP server that pretends to be the target node
	var receivedPath string
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("target-response"))
	}))
	defer targetSrv.Close()

	// 2. Setup the router with a PeerStore that knows about "node-target"
	router := &p2p.P2PRoundTripper{
		Base: http.DefaultTransport,
	}
	router.UpdatePeerRoute("node-target", protocol.AddressRecord{
		Addresses: []string{targetSrv.URL},
		Sequence:  1,
	})

	client := &http.Client{
		Transport: router,
	}

	// 3. Make a request using the proxyma:// virtual scheme
	req, err := http.NewRequest(http.MethodGet, "http://node-target.proxyma.local/some/api/path", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// 4. Verify it was correctly routed
	require.Equal(t, http.StatusOK, resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "target-response", string(bodyBytes))
	require.Equal(t, "/some/api/path", receivedPath)
}

func TestP2PRoundTripperRelayFallback(t *testing.T) {
	t.Parallel()

	// 1. Setup a real HTTP server that pretends to be the SPONSOR
	var fwdReceived bool
	sponsorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/relay/forward" {
			fwdReceived = true

			// Simulate a successful relay response
			respBody := `{"req_id":"test-123","status_code":200,"headers":{"X-Test":"Ok"},"body":"UmVsYXkgT0s="}` // Base64 of "Relay OK"
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(respBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sponsorSrv.Close()

	// 2. Setup the router with a PeerStore that knows about "node-target" (with an unreachable direct IP)
	router := &p2p.P2PRoundTripper{
		SponsorAddress: sponsorSrv.URL,
		Base:           http.DefaultTransport,
	}
	router.UpdatePeerRoute("node-target", protocol.AddressRecord{
		Addresses: []string{"http://127.0.0.1:0"}, // Unreachable
		Sequence:  1,
	})

	client := &http.Client{
		Transport: router,
	}

	// 3. Make a request using the proxyma:// virtual scheme
	req, err := http.NewRequest(http.MethodGet, "http://node-target.proxyma.local/some/api", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// 4. Verify it fell back to the sponsor
	require.True(t, fwdReceived, "Sponsor should have received a /relay/forward request")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "Relay OK", string(bodyBytes))
}

func TestP2PRoundTripperPeerIdentityMismatch(t *testing.T) {
	// 1. Generate CA and certificates for 'bob'
	caPath := t.TempDir()
	err := p2p.InitCluster(caPath)
	require.NoError(t, err)

	err = p2p.IssueNodeCertificate(caPath, caPath, "bob")
	require.NoError(t, err)

	caCertFile, _ := p2p.CACertPaths(caPath)
	nodeCertFile, nodeKeyFile := p2p.NodeCertPaths(caPath, "bob")

	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)

	// 2. Start a secure TLS server presenting 'bob''s certificate
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hijacked-response"))
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()

	// 3. Configure the router to dial 'alice' at 'bob''s server URL
	router := &p2p.P2PRoundTripper{
		Base: &http.Transport{
			TLSClientConfig: clientTLS,
		},
	}
	router.UpdatePeerRoute("alice", protocol.AddressRecord{
		Addresses: []string{server.URL},
		Sequence:  1,
	})

	client := &http.Client{
		Transport: router,
	}

	// 4. Request 'alice'
	req, err := http.NewRequest(http.MethodGet, "http://alice.proxyma.local/some/path", nil)
	require.NoError(t, err)

	// This should fail because the direct connection will reject 'bob''s certificate for 'alice'
	_, err = client.Do(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer identity mismatch")
}

func TestRelayFallbackPreservesURLQuery(t *testing.T) {
	t.Parallel()

	var forwarded protocol.RelayRequest
	sponsorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.PathRelayForward {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&forwarded))
		respBody := `{"req_id":"test-query","status_code":200,"headers":{},"body":"T0s="}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(sponsorSrv.Close)

	router := &p2p.P2PRoundTripper{
		SponsorAddress: sponsorSrv.URL,
		Base:           http.DefaultTransport,
	}
	router.UpdatePeerRoute("node", protocol.AddressRecord{
		Addresses: []string{"http://127.0.0.1:0"},
		Sequence:  1,
	})

	client := &http.Client{Transport: router}
	req, err := http.NewRequest(http.MethodPost, "http://node.proxyma.local/services/stream?service=ocr", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	_, _ = io.Copy(io.Discard, resp.Body)

	require.Equal(t, "/services/stream?service=ocr", forwarded.Path)
}
