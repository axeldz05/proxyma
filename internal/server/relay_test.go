package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type countingReader struct {
	r io.Reader
	n int
}

type relayHTTPResult struct {
	resp *http.Response
	err  error
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

func TestRelayLongPollingIntegration(t *testing.T) {
	t.Parallel()

	// 1. Start Server
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-relay"), nil)

	// Register target-node as a known peer of the sponsor
	sponsor.AddPeer("target-node", protocol.AddressRecord{
		Addresses: []string{"http://target-node.proxyma.local"},
	})

	// Issue and load certificate for target-node to simulate mTLS authentication
	caPath := filepath.Dir(sponsor.Config.StoragePath)
	targetClientTLS := testutil.IssueNode(t, caPath, sponsor.Config.StoragePath, "target-node").ClientTLS

	targetClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   targetClientTLS,
			DisableKeepAlives: true,
		},
	}

	// 2. Start a long poll from "target-node" using its specific mTLS client
	pollReq, _ := http.NewRequest(http.MethodGet, sponsor.Config.Address+protocol.PathRelayPoll+"?id=target-node", nil)
	pollCtx, pollCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pollCancel()
	pollReq = pollReq.WithContext(pollCtx)

	pollResultCh := make(chan relayHTTPResult, 1)
	go func() {
		resp, err := targetClient.Do(pollReq)
		pollResultCh <- relayHTTPResult{resp: resp, err: err}
	}()

	// 3. Send a forward request from "sender-node" destined to "target-node"
	relayReq := protocol.RelayRequest{
		ReqID:   "req-123",
		Target:  "target-node",
		Method:  "GET",
		Path:    "/some/test/path",
		Headers: map[string]string{"X-Test": "Hello"},
		Body:    []byte("hello relay"),
	}
	bodyBytes, _ := json.Marshal(relayReq)

	fwdReq, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+protocol.PathRelayForward, bytes.NewBuffer(bodyBytes))
	fwdReq.Header.Set("Content-Type", "application/json")

	fwdResultCh := make(chan relayHTTPResult, 1)
	go func() {
		resp, err := sponsor.Client().Do(fwdReq)
		fwdResultCh <- relayHTTPResult{resp: resp, err: err}
	}()

	// 4. target-node's poll should complete and receive the request
	var pollResult relayHTTPResult
	select {
	case pollResult = <-pollResultCh:
	case <-pollCtx.Done():
		t.Fatal("timeout waiting for target relay poll")
	}
	require.NoError(t, pollResult.err)
	pollResp := pollResult.resp
	require.NotNil(t, pollResp)
	require.Equal(t, http.StatusOK, pollResp.StatusCode)

	var receivedReq protocol.RelayRequest
	err := json.NewDecoder(pollResp.Body).Decode(&receivedReq)
	require.NoError(t, err)
	require.Equal(t, "req-123", receivedReq.ReqID)
	require.Equal(t, "/some/test/path", receivedReq.Path)
	_ = pollResp.Body.Close()

	// 5. target-node sends the reply back
	relayRes := protocol.RelayResponse{
		ReqID:      "req-123",
		StatusCode: 201,
		Headers:    map[string]string{"X-Reply": "World"},
		Body:       []byte("response relay"),
	}
	resBytes, _ := json.Marshal(relayRes)

	replyReq, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+protocol.PathRelayReply, bytes.NewBuffer(resBytes))
	replyReq.Header.Set("Content-Type", "application/json")
	replyResp, err := targetClient.Do(replyReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replyResp.StatusCode)
	_ = replyResp.Body.Close()

	// 6. sender-node's forward request should complete with the response
	var fwdResult relayHTTPResult
	select {
	case fwdResult = <-fwdResultCh:
	case <-pollCtx.Done():
		t.Fatal("timeout waiting for relay forward response")
	}
	require.NoError(t, fwdResult.err)
	fwdResp := fwdResult.resp
	require.NotNil(t, fwdResp)
	require.Equal(t, http.StatusOK, fwdResp.StatusCode)

	var finalRes protocol.RelayResponse
	err = json.NewDecoder(fwdResp.Body).Decode(&finalRes)
	require.NoError(t, err)
	require.Equal(t, 201, finalRes.StatusCode)
	require.Equal(t, "response relay", string(finalRes.Body))
	_ = fwdResp.Body.Close()
}

func TestAdaptiveRelayPollingAndFailover(t *testing.T) {
	t.Parallel()

	firstPoll := make(chan struct{}, 1)
	firstSponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case protocol.PathRelayPoll:
			select {
			case firstPoll <- struct{}{}:
			default:
			}
			http.Error(w, "poll failed", http.StatusBadGateway)
		case protocol.PathRelayForward:
			http.Error(w, "relay failed", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(firstSponsor.Close)

	secondForward := make(chan protocol.RelayRequest, 1)
	handlerErr := make(chan error, 1)
	secondSponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case protocol.PathRelayPoll:
			w.WriteHeader(http.StatusNoContent)
		case protocol.PathRelayForward:
			var req protocol.RelayRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			select {
			case secondForward <- req:
			default:
			}
			if err := json.NewEncoder(w).Encode(protocol.RelayResponse{
				ReqID:      req.ReqID,
				StatusCode: http.StatusOK,
				Body:       []byte(`{}`),
			}); err != nil {
				select {
				case handlerErr <- err:
				default:
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(secondSponsor.Close)

	cfg := testutil.DefaultConfig(t, "adaptive-client")
	cfg.BootstrapNode = firstSponsor.URL
	peerClient := p2p.NewHTTPPeerClient(http.DefaultTransport, firstSponsor.URL, cfg.Logger)
	srv := NewServer(t, cfg, peerClient)

	srv.AddPeer("target", protocol.AddressRecord{
		Addresses: []string{"http://127.0.0.1:0"},
		Sequence:  1,
	})
	srv.AddPeer("sponsor1", protocol.AddressRecord{
		Addresses: []string{firstSponsor.URL},
		Sequence:  1,
		IsSponsor: true,
	})
	srv.AddPeer("sponsor2", protocol.AddressRecord{
		Addresses: []string{secondSponsor.URL},
		Sequence:  1,
		IsSponsor: true,
	})

	srv.SetPeerOnline("sponsor1", true)
	srv.SetPeerOnline("sponsor2", true)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pollingDone := make(chan struct{})
	go func() {
		defer close(pollingDone)
		srv.StartRelayPolling(ctx, firstSponsor.URL)
	}()

	select {
	case <-firstPoll:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timeout waiting for first sponsor poll")
	}

	require.Eventually(t, func() bool {
		callCtx, callCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer callCancel()
		_, err := srv.PeerClient().FetchManifest(callCtx, "target")
		return err == nil
	}, 2*time.Second, 20*time.Millisecond,
		"outbound relay routing must follow the polling failover sponsor")

	select {
	case req := <-secondForward:
		require.Equal(t, "target", req.Target)
		require.Equal(t, protocol.PathManifest, req.Path)
	case err := <-handlerErr:
		t.Fatalf("failover sponsor response failed: %v", err)
	default:
		t.Fatal("successful outbound relay did not reach the failover sponsor")
	}

	cancel()
	select {
	case <-pollingDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay polling did not stop after cancellation")
	}
}

func TestRelayPollingPublishesInitialSponsorToRouter(t *testing.T) {
	t.Parallel()

	staleSponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stale sponsor", http.StatusBadGateway)
	}))
	t.Cleanup(staleSponsor.Close)

	polled := make(chan struct{}, 1)
	forwarded := make(chan protocol.RelayRequest, 1)
	handlerErr := make(chan error, 1)
	activeSponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case protocol.PathRelayPoll:
			select {
			case polled <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case protocol.PathRelayForward:
			var req protocol.RelayRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			forwarded <- req
			if err := json.NewEncoder(w).Encode(protocol.RelayResponse{
				ReqID:      req.ReqID,
				StatusCode: http.StatusOK,
				Body:       []byte(`{}`),
			}); err != nil {
				handlerErr <- err
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(activeSponsor.Close)

	cfg := testutil.DefaultConfig(t, "initial-sponsor-client")
	cfg.BootstrapNode = staleSponsor.URL
	peerClient := p2p.NewHTTPPeerClient(http.DefaultTransport, staleSponsor.URL, cfg.Logger)
	srv := NewServer(t, cfg, peerClient)
	srv.AddPeer("target", protocol.AddressRecord{
		Addresses: []string{"http://127.0.0.1:0"},
		Sequence:  1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pollingDone := make(chan struct{})
	go func() {
		defer close(pollingDone)
		srv.StartRelayPolling(ctx, activeSponsor.URL)
	}()

	select {
	case <-polled:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial sponsor poll")
	}

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	_, err := srv.PeerClient().FetchManifest(callCtx, "target")
	require.NoError(t, err, "outbound router must use StartRelayPolling's initial sponsor")

	select {
	case req := <-forwarded:
		require.Equal(t, "target", req.Target)
	case err := <-handlerErr:
		t.Fatalf("active sponsor response failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("initial sponsor did not receive outbound relay")
	}

	cancel()
	select {
	case <-pollingDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay polling did not stop")
	}
}

func TestRelayResponseEnforcesSizeCap(t *testing.T) {
	t.Parallel()

	sv := NewServer(t, testutil.DefaultConfig(t, "relay-cap"), nil)

	huge := protocol.RelayResponse{
		ReqID:      "cap-1",
		StatusCode: 200,
		Body:       bytes.Repeat([]byte("B"), protocol.MaxRelayBodyBytes+1),
	}
	body, _ := json.Marshal(huge)
	req, err := http.NewRequest(http.MethodPost, sv.Config.Address+protocol.PathRelayReply, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)

	var mu sync.Mutex
	var got protocol.RelayResponse
	mock := &testutil.MockPeerClient{
		OnReplyRelay: func(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
			mu.Lock()
			got = resp
			mu.Unlock()
			return nil
		},
	}
	sv2 := NewServer(t, testutil.DefaultConfig(t, "relay-cap-proc"), mock)
	var handlerWritten int
	var handlerWriteErr error
	sv2.SetHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Relay-Header", "preserved")
		w.WriteHeader(http.StatusCreated)
		handlerWritten, handlerWriteErr = w.Write(bytes.Repeat([]byte("X"), protocol.MaxRelayBodyBytes*4))
	}))
	sv2.ProcessRelayRequest("https://sponsor.invalid", protocol.RelayRequest{
		ReqID:        "cap-proc",
		Method:       http.MethodGet,
		Path:         "/huge",
		OriginPeerID: "alice",
	})
	mu.Lock()
	defer mu.Unlock()
	require.Error(t, handlerWriteErr, "the local handler must be stopped while writing past the cap")
	require.LessOrEqual(t, handlerWritten, protocol.MaxRelayBodyBytes)
	require.Equal(t, http.StatusRequestEntityTooLarge, got.StatusCode)
	require.Equal(t, "preserved", got.Headers["X-Relay-Header"])
	require.LessOrEqual(t, len(got.Body), protocol.MaxRelayBodyBytes)
}

func TestRelayForwardBoundsOversizedJSONBeforeDecode(t *testing.T) {
	t.Parallel()

	sv := NewServer(t, testutil.DefaultConfig(t, "relay-request-read-cap"), nil)
	payload, err := json.Marshal(protocol.RelayRequest{
		ReqID:  "oversized-request",
		Target: sv.Config.ID,
		Method: http.MethodPost,
		Path:   protocol.PathClusterJoin,
		Body:   bytes.Repeat([]byte("X"), protocol.MaxRelayBodyBytes*8),
	})
	require.NoError(t, err)

	source := &countingReader{r: bytes.NewReader(payload)}
	req := httptest.NewRequest(http.MethodPost, protocol.PathRelayForward, source)
	rec := httptest.NewRecorder()

	sv.HandleRelayForward(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.LessOrEqual(t, source.n, 2*protocol.MaxRelayBodyBytes+1,
		"relay JSON decoding must stop at a bounded envelope size")
}

func TestProcessRelayRequestRejectsMalformedConstruction(t *testing.T) {
	t.Parallel()

	var replies []protocol.RelayResponse
	mock := &testutil.MockPeerClient{
		OnReplyRelay: func(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
			replies = append(replies, resp)
			return nil
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "relay-malformed"), mock)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "malformed method on anonymous join",
			method: "POST invalid",
			path:   protocol.PathClusterJoin,
		},
		{
			name:   "malformed request URL",
			method: http.MethodPost,
			path:   "/invalid/%zz",
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				sv.ProcessRelayRequest("https://sponsor.invalid", protocol.RelayRequest{
					ReqID:  fmt.Sprintf("malformed-%d", i),
					Method: tt.method,
					Path:   tt.path,
				})
			})
			require.Len(t, replies, i+1)
			require.Equal(t, http.StatusBadRequest, replies[i].StatusCode)
			require.Equal(t, fmt.Sprintf("malformed-%d", i), replies[i].ReqID)
			require.Contains(t, string(replies[i].Body), "invalid relay request")
		})
	}
}

func TestProcessRelayResponsePreservesStatusAndHeaders(t *testing.T) {
	t.Parallel()

	var got protocol.RelayResponse
	mock := &testutil.MockPeerClient{
		OnReplyRelay: func(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
			got = resp
			return nil
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "relay-response-metadata"), mock)
	var handlerErr error
	sv.SetHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Relay-Header", "value")
		w.WriteHeader(http.StatusCreated)
		_, handlerErr = w.Write([]byte("created"))
	}))

	sv.ProcessRelayRequest("https://sponsor.invalid", protocol.RelayRequest{
		ReqID:        "metadata",
		Method:       http.MethodGet,
		Path:         "/metadata",
		OriginPeerID: "alice",
	})

	require.NoError(t, handlerErr)
	require.Equal(t, http.StatusCreated, got.StatusCode)
	require.Equal(t, "value", got.Headers["X-Relay-Header"])
	require.Equal(t, "created", string(got.Body))
}

func TestRelayedRequestPreservesOriginIdentity(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var lastStatus int
	mock := &testutil.MockPeerClient{
		OnReplyRelay: func(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
			mu.Lock()
			lastStatus = resp.StatusCode
			mu.Unlock()
			return nil
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "relay-origin"), mock)
	sv.AddPeer("alice", protocol.AddressRecord{Addresses: []string{"https://alice.invalid"}})
	require.True(t, sv.IsPeerOnline("alice"))

	sv.ProcessRelayRequest("https://sponsor.invalid", protocol.RelayRequest{
		ReqID:        "origin-1",
		Method:       http.MethodPost,
		Path:         protocol.PathPeersOffline,
		OriginPeerID: "alice",
		Headers:      map[string]string{"Content-Type": "application/json"},
		Body:         []byte(`{"id":"alice"}`),
	})
	require.False(t, sv.IsPeerOnline("alice"), "CN=alice must authorize offline for alice, not self")
	mu.Lock()
	require.Equal(t, http.StatusOK, lastStatus)
	mu.Unlock()

	sv.AddPeer("bob", protocol.AddressRecord{Addresses: []string{"https://bob.invalid"}})
	require.True(t, sv.IsPeerOnline("bob"))
	sv.ProcessRelayRequest("https://sponsor.invalid", protocol.RelayRequest{
		ReqID:   "origin-2",
		Method:  http.MethodPost,
		Path:    protocol.PathPeersOffline,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"id":"bob"}`),
	})
	require.True(t, sv.IsPeerOnline("bob"), "missing origin must not authorize offline")
	mu.Lock()
	require.Equal(t, http.StatusForbidden, lastStatus)
	mu.Unlock()
}
