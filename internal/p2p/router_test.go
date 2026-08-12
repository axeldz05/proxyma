package p2p_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type routerRoundTripper func(*http.Request) (*http.Response, error)

func (f routerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (f routerRoundTripper) RoundTripPeerVerified(req *http.Request, _ string) (*http.Response, error) {
	return f(req)
}

func TestP2PDirectProbeHonorsRequestCancellation(t *testing.T) {
	t.Parallel()

	probeStarted := make(chan time.Duration, 1)
	router := &p2p.P2PRoundTripper{
		Base: routerRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("HTTP request must not start after probe cancellation")
		}),
		ProbeDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return nil, fmt.Errorf("route probe context has no upper bound")
			}
			probeStarted <- time.Until(deadline)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	router.UpdatePeerRoute("blocked", protocol.AddressRecord{
		Addresses: []string{"https://blocked.invalid:443"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://blocked.proxyma.local/health", nil)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, err := (&http.Client{Transport: router}).Do(req)
		result <- err
	}()

	select {
	case probeBudget := <-probeStarted:
		require.Positive(t, probeBudget)
		require.LessOrEqual(t, probeBudget, protocol.DialTimeoutRouteProbe)
	case <-time.After(2 * time.Second):
		t.Fatal("route probe did not start")
	}
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled route probe did not abort promptly")
	}
}

func TestP2PDirectRouteRejectsPlainHTTP(t *testing.T) {
	t.Parallel()

	requestReceived := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	router := &p2p.P2PRoundTripper{Base: http.DefaultTransport}
	router.UpdatePeerRoute("alice", protocol.AddressRecord{Addresses: []string{target.URL}})
	req, err := http.NewRequest(http.MethodGet, "http://alice.proxyma.local/private", nil)
	require.NoError(t, err)

	_, err = (&http.Client{Transport: router}).Do(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTPS")
	select {
	case <-requestReceived:
		t.Fatal("plain HTTP route bypassed peer certificate verification")
	default:
	}
}

func TestP2PDirectProbeDoesNotLimitLargeRequest(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("L"), protocol.MaxRelayBodyBytes+1)
	var received int
	router := &p2p.P2PRoundTripper{
		ProbeDialContext: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
		Base: routerRoundTripper(func(req *http.Request) (*http.Response, error) {
			if _, ok := req.Context().Deadline(); ok {
				return nil, fmt.Errorf("probe timeout leaked into direct HTTP request")
			}
			payload, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			received = len(payload)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(nil)),
				TLS: &tls.ConnectionState{
					PeerCertificates: []*x509.Certificate{{
						Subject: pkix.Name{CommonName: "large-peer"},
					}},
				},
			}, nil
		}),
	}
	router.UpdatePeerRoute("large-peer", protocol.AddressRecord{
		Addresses: []string{"https://large.invalid:443"},
	})
	req, err := http.NewRequest(http.MethodPost, "http://large-peer.proxyma.local/upload", bytes.NewReader(body))
	require.NoError(t, err)

	resp, err := (&http.Client{Transport: router}).Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, len(body), received)
}

func TestP2PRoundTripperDirectRouting(t *testing.T) {
	t.Parallel()

	// 1. Setup a real mTLS server for the target node.
	nodeTLS := testutil.NewNodeTLS(t, "node-target")
	receivedPath := make(chan string, 1)
	targetSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath <- r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("target-response"))
	}))
	targetSrv.TLS = nodeTLS.ServerTLS
	targetSrv.StartTLS()
	t.Cleanup(targetSrv.Close)

	// 2. Setup the router with a PeerStore that knows about "node-target"
	router := &p2p.P2PRoundTripper{
		Base: &http.Transport{TLSClientConfig: nodeTLS.ClientTLS},
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
	select {
	case path := <-receivedPath:
		require.Equal(t, "/some/api/path", path)
	case <-time.After(2 * time.Second):
		t.Fatal("direct target did not receive request")
	}
}

func TestP2PRoundTripperRelayFallback(t *testing.T) {
	t.Parallel()

	// 1. Setup a real HTTP server that pretends to be the SPONSOR
	var fwdReceived bool
	sponsorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.PathRelayForward {
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
	t.Parallel()

	// 1. Generate CA and certificates for 'bob'
	bob := testutil.NewNodeTLS(t, "bob")
	serverTLS, clientTLS := bob.ServerTLS, bob.ClientTLS

	// 2. Start a secure TLS server presenting 'bob''s certificate
	requestReceived := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived <- struct{}{}
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
		Timeout:   2 * time.Second,
	}

	// 4. Request 'alice'
	req, err := http.NewRequest(http.MethodPost, "http://alice.proxyma.local/some/path", strings.NewReader("sensitive request body"))
	require.NoError(t, err)

	// Identity rejection must happen in the TLS handshake, before HTTP headers or
	// the request body can reach the wrong peer.
	_, err = client.Do(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer identity mismatch")
	select {
	case <-requestReceived:
		t.Fatal("identity-mismatched peer received HTTP data before rejection")
	default:
	}
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

	require.Equal(t, protocol.PathServicesStream+"?service=ocr", forwarded.Path)
}

func TestP2PRoundTripperPreservesBodyAfterFailedRelay(t *testing.T) {
	t.Parallel()

	nodeTLS := testutil.NewNodeTLS(t, "node-target")
	gotBody := make(chan string, 1)
	directSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody <- string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct-ok"))
	}))
	directSrv.TLS = nodeTLS.ServerTLS
	directSrv.StartTLS()
	t.Cleanup(directSrv.Close)

	sponsorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.PathRelayForward {
			http.Error(w, "relay down", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sponsorSrv.Close()

	router := &p2p.P2PRoundTripper{
		SponsorAddress: sponsorSrv.URL,
		Base:           &http.Transport{TLSClientConfig: nodeTLS.ClientTLS},
	}
	router.UpdatePeerRoute("node-target", protocol.AddressRecord{
		Addresses: []string{
			"https://unreachable.invalid:9",
			directSrv.URL,
		},
		Sequence: 1,
	})

	client := &http.Client{Transport: router}
	req, err := http.NewRequest(http.MethodPost, "http://node-target.proxyma.local/upload", bytes.NewBufferString(`{"blob":"payload"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	select {
	case body := <-gotBody:
		require.Equal(t, `{"blob":"payload"}`, body, "body must survive failed relay for Phase-3 direct dial")
	case <-time.After(2 * time.Second):
		t.Fatal("phase-3 direct target did not receive request")
	}
}
