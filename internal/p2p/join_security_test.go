package p2p_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func joinTestTLS(t *testing.T) (*tls.Config, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, p2p.InitCluster(dir))
	require.NoError(t, p2p.IssueNodeCertificate(dir, dir, "join-sponsor"))
	caPath, _ := p2p.CACertPaths(dir)
	certPath, keyPath := p2p.NodeCertPaths(dir, "join-sponsor")
	serverTLS, _, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	require.NoError(t, err)
	caPEM, err := p2p.ReadCAPEM(caPath)
	require.NoError(t, err)
	caHash, err := p2p.CAHashFromPEM(caPEM)
	require.NoError(t, err)
	return serverTLS, caHash
}

func startJoinTLSServer(t *testing.T, tlsConfig *tls.Config, handler http.Handler) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)
	server.TLS = tlsConfig.Clone()
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func legacyJoinToken(t *testing.T, payload p2p.InvitePayload, secret string) string {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + secret
}

func v2JoinToken(t *testing.T, caHash string, port uint16, relayAddr string) (token, secret string) {
	t.Helper()

	hash, err := hex.DecodeString(caHash)
	require.NoError(t, err)
	require.Len(t, hash, 32)
	secretBytes := bytes.Repeat([]byte{0xa5}, 32)

	var tokenBytes bytes.Buffer
	tokenBytes.WriteByte(2)
	tokenBytes.Write(hash)
	tokenBytes.Write(secretBytes)
	require.NoError(t, binary.Write(&tokenBytes, binary.BigEndian, port))
	tokenBytes.WriteByte(3)

	// Deliberately pack the stale IP first. JoinCluster must still try the
	// hostname, then relay, and never return to the IP after relay succeeds.
	tokenBytes.WriteByte(1)
	tokenBytes.Write(net.IPv4(127, 0, 0, 1).To4())
	tokenBytes.WriteByte(3)
	tokenBytes.WriteByte(byte(len("localhost")))
	tokenBytes.WriteString("localhost")
	tokenBytes.WriteByte(4)
	tokenBytes.WriteByte(byte(len("join-sponsor")))
	tokenBytes.WriteString("join-sponsor")
	tokenBytes.WriteByte(byte(len(relayAddr)))
	tokenBytes.WriteString(relayAddr)

	return base64.RawURLEncoding.EncodeToString(tokenBytes.Bytes()), hex.EncodeToString(secretBytes)
}

func TestJoinFailureDoesNotExposeInviteSecret(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("0123456789abcdef", 4)
	token := legacyJoinToken(t, p2p.InvitePayload{
		Address: "https://127.0.0.1:0",
		CAHash:  strings.Repeat("b", 64),
	}, secret)

	var logMu sync.Mutex
	var logs []string
	_, _, _, _, err := p2p.JoinCluster(context.Background(), token, "joining-node", "https://joining-node:8443", func(msg string, logErr error) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, msg)
		if logErr != nil {
			logs = append(logs, logErr.Error())
		}
	})
	require.Error(t, err)

	output := err.Error() + "\n" + strings.Join(logs, "\n")
	require.NotContains(t, output, secret)
	require.NotContains(t, output, secret[:8])
}

func TestJoinNeverSendsInviteSecretOverPlainHTTP(t *testing.T) {
	t.Parallel()

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received <- struct{}{}
	}))
	t.Cleanup(server.Close)

	secret := strings.Repeat("d", 64)
	token := legacyJoinToken(t, p2p.InvitePayload{
		Address: server.URL,
		CAHash:  strings.Repeat("e", 64),
	}, secret)
	_, _, _, _, err := p2p.JoinCluster(context.Background(), token, "joining-node", "https://joining-node:8443", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTPS")
	select {
	case <-received:
		t.Fatal("JoinCluster sent the invite secret over plain HTTP")
	default:
	}
}

func TestJoinRejectsSponsorWhoseCADoesNotMatchToken(t *testing.T) {
	t.Parallel()

	serverTLS, _ := joinTestTLS(t)
	handlerCalled := make(chan struct{}, 1)
	sponsor := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled <- struct{}{}
	}))

	otherCADir := t.TempDir()
	require.NoError(t, p2p.InitCluster(otherCADir))
	otherCAPath, _ := p2p.CACertPaths(otherCADir)
	otherCAPEM, err := p2p.ReadCAPEM(otherCAPath)
	require.NoError(t, err)
	otherCAHash, err := p2p.CAHashFromPEM(otherCAPEM)
	require.NoError(t, err)

	token := legacyJoinToken(t, p2p.InvitePayload{
		Address: sponsor.URL,
		CAHash:  otherCAHash,
	}, strings.Repeat("b", 64))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	caCert, cert, key, bootstrap, err := p2p.JoinCluster(
		ctx,
		token,
		"joining-node",
		"https://joining-node:8443",
		nil,
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "pinned CA")
	require.Empty(t, caCert)
	require.Empty(t, cert)
	require.Empty(t, key)
	require.Empty(t, bootstrap)
	select {
	case <-handlerCalled:
		t.Fatal("join request reached a sponsor whose CA did not match the token pin")
	default:
	}
}

func TestJoinRejectsRedirectBeforeInviteSecretReplay(t *testing.T) {
	t.Parallel()

	replayed := make(chan struct{}, 1)
	plaintextTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		replayed <- struct{}{}
	}))
	t.Cleanup(plaintextTarget.Close)

	serverTLS, caHash := joinTestTLS(t)
	redirector := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", plaintextTarget.URL+protocol.PathClusterJoin)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	secret := strings.Repeat("a", 64)
	token := legacyJoinToken(t, p2p.InvitePayload{
		Address: redirector.URL,
		CAHash:  caHash,
	}, secret)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, _, err := p2p.JoinCluster(ctx, token, "joining-node", "https://joining-node:8443", nil)
	require.Error(t, err)

	select {
	case <-replayed:
		t.Fatal("JoinCluster followed a redirect and replayed the invite secret")
	default:
	}
}

func TestJoinRelayRejectionDoesNotLogSecretEcho(t *testing.T) {
	t.Parallel()

	serverTLS, caHash := joinTestTLS(t)
	secret := strings.Repeat("feedface", 8)
	relay := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.RelayResponse{
			ReqID:      "rejected-join",
			StatusCode: http.StatusForbidden,
			Body:       []byte("rejected secret " + secret[:8]),
		})
	}))
	token := legacyJoinToken(t, p2p.InvitePayload{
		Address:   "https://127.0.0.1:0",
		CAHash:    caHash,
		SponsorID: "join-sponsor",
		RelayAddr: relay.URL,
	}, secret)

	var logMu sync.Mutex
	var logs []string
	_, _, _, _, err := p2p.JoinCluster(context.Background(), token, "joining-node", "https://joining-node:8443", func(msg string, logErr error) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, msg)
		if logErr != nil {
			logs = append(logs, logErr.Error())
		}
	})
	require.Error(t, err)
	output := err.Error() + "\n" + strings.Join(logs, "\n")
	require.NotContains(t, output, secret[:8])
}

func TestJoinUsesHostnameThenRelayBeforePackedIPLiteral(t *testing.T) {
	t.Parallel()

	serverTLS, caHash := joinTestTLS(t)
	events := make(chan string, 4)
	var directCalls atomic.Int32
	direct := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		events <- "direct"
		http.Error(w, "direct unavailable", http.StatusServiceUnavailable)
	}))

	joinBody, err := json.Marshal(protocol.JoinResponse{
		CACert:      "joined-ca",
		Certificate: "joined-cert",
	})
	require.NoError(t, err)
	relayPath := make(chan string, 1)
	relay := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events <- "relay"
		relayPath <- r.URL.Path
		_ = json.NewEncoder(w).Encode(protocol.RelayResponse{
			ReqID:      "join-relay",
			StatusCode: http.StatusOK,
			Body:       joinBody,
		})
	}))

	directURL, err := url.Parse(direct.URL)
	require.NoError(t, err)
	_, portString, err := net.SplitHostPort(directURL.Host)
	require.NoError(t, err)
	port, err := strconv.ParseUint(portString, 10, 16)
	require.NoError(t, err)
	token, _ := v2JoinToken(t, caHash, uint16(port), relay.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	caCert, cert, key, bootstrap, err := p2p.JoinCluster(ctx, token, "joining-node", "https://joining-node:8443", nil)
	require.NoError(t, err)
	require.Equal(t, "joined-ca", caCert)
	require.Equal(t, "joined-cert", cert)
	require.NotEmpty(t, key)
	require.Equal(t, relay.URL, bootstrap)
	require.Equal(t, int32(1), directCalls.Load(), "stale IP literal must not be attempted before a successful relay")
	require.Equal(t, protocol.PathRelayForward, <-relayPath)

	require.Equal(t, "direct", <-events)
	require.Equal(t, "relay", <-events)
	select {
	case event := <-events:
		t.Fatalf("unexpected extra join attempt after relay success: %s", event)
	default:
	}
}

func TestJoinCancellationStopsFallbackAttempts(t *testing.T) {
	t.Parallel()

	serverTLS, caHash := joinTestTLS(t)
	directStarted := make(chan struct{})
	releaseDirect := make(chan struct{})
	var startOnce sync.Once
	direct := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(directStarted) })
		<-releaseDirect
	}))
	var relayCalls atomic.Int32
	relay := startJoinTLSServer(t, serverTLS, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		relayCalls.Add(1)
	}))

	directURL, err := url.Parse(direct.URL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(directURL.Host)
	require.NoError(t, err)
	secret := strings.Repeat("c", 64)
	token := legacyJoinToken(t, p2p.InvitePayload{
		Address:   "https://" + net.JoinHostPort("localhost", port),
		CAHash:    caHash,
		SponsorID: "join-sponsor",
		RelayAddr: relay.URL,
	}, secret)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, _, _, joinErr := p2p.JoinCluster(ctx, token, "joining-node", "https://joining-node:8443", nil)
		result <- joinErr
	}()

	select {
	case <-directStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("direct join attempt did not start")
	}
	cancel()

	var joinErr error
	select {
	case joinErr = <-result:
	case <-time.After(500 * time.Millisecond):
		close(releaseDirect)
		t.Fatal("JoinCluster did not return promptly after cancellation")
	}
	close(releaseDirect)
	require.ErrorIs(t, joinErr, context.Canceled)
	require.Zero(t, relayCalls.Load())
}
