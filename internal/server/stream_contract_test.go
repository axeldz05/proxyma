package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"proxyma/internal/utils"
)

type streamContractPeerClient struct {
	*testutil.MockPeerClient
	stream            func(context.Context, string, string, map[string]any) (io.ReadCloser, error)
	negotiatedVersion *int
}

func (c *streamContractPeerClient) StreamService(
	ctx context.Context,
	peerID string,
	serviceName string,
	payload map[string]any,
) (io.ReadCloser, error) {
	return c.stream(ctx, peerID, serviceName, payload)
}

func (c *streamContractPeerClient) StreamServiceNegotiated(
	ctx context.Context,
	peerID string,
	serviceName string,
	payload map[string]any,
) (p2p.NegotiatedServiceStream, error) {
	body, err := c.stream(ctx, peerID, serviceName, payload)
	version := protocol.ServiceStreamVersion
	if c.negotiatedVersion != nil {
		version = *c.negotiatedVersion
	}
	return p2p.NegotiatedServiceStream{Body: body, Version: version}, err
}

func newStreamContractServer(t *testing.T, client p2p.PeerClient) *Server {
	t.Helper()
	srv, err := New(protocol.NodeConfig{
		ID:          "stream-contract",
		StoragePath: t.TempDir(),
		Workers:     1,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, client)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown server: %v", err)
		}
	})
	return srv
}

func registerContractStream(
	t *testing.T,
	srv *Server,
	name string,
	handler func(context.Context, <-chan map[string]any, chan<- map[string]any, map[string]any) (map[string]any, error),
) {
	t.Helper()
	if err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: name,
		Type: protocol.ServiceTypeBidi,
	}, handler); err != nil {
		t.Fatalf("register stream service: %v", err)
	}
}

func serviceStreamFrameLine(t *testing.T, frame protocol.ServiceStreamFrame) string {
	t.Helper()
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal service stream frame: %v", err)
	}
	return string(encoded) + "\n"
}

func TestHTTPStreamFramesRuntimeFailureAsTerminalError(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "runtime-error", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"n": 1}
		return nil, errors.New("stream exploded")
	})

	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "runtime-error"),
		strings.NewReader(`{"input":"x"}`),
	)
	req.Header.Set(protocol.HeaderStreamAcceptVersions, "1")
	rec := httptest.NewRecorder()
	srv.HandleServicesStream(rec, req)
	if selected := rec.Header().Get(protocol.HeaderStreamSelectedVersion); selected != "1" {
		t.Fatalf("selected stream version = %q, want 1", selected)
	}

	var frames []map[string]any
	if err := utils.ForEachNDJSON(rec.Body, func(frame map[string]any) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %#v, want chunk + terminal error", frames)
	}
	if frames[0]["proxyma_stream_version"] != float64(protocol.ServiceStreamVersion) ||
		frames[0]["kind"] != string(protocol.ServiceStreamFrameChunk) ||
		frames[0]["chunk"].(map[string]any)["n"] != float64(1) {
		t.Fatalf("first frame = %#v, want service chunk", frames[0])
	}
	if frames[1]["kind"] != string(protocol.ServiceStreamFrameError) ||
		frames[1]["error"] != "stream exploded" {
		t.Fatalf("terminal frame = %#v, want explicit stream error", frames[1])
	}
}

func TestHTTPStreamEmitsExplicitCompletionFrame(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "http-success", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"n": 1}
		return nil, nil
	})

	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "http-success"),
		strings.NewReader(`{}`),
	)
	req.Header.Set(protocol.HeaderStreamAcceptVersions, "1")
	rec := httptest.NewRecorder()
	srv.HandleServicesStream(rec, req)
	if selected := rec.Header().Get(protocol.HeaderStreamSelectedVersion); selected != "1" {
		t.Fatalf("selected stream version = %q, want 1", selected)
	}

	var frames []map[string]any
	if err := utils.ForEachNDJSON(rec.Body, func(frame map[string]any) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if len(frames) != 2 ||
		frames[1]["kind"] != string(protocol.ServiceStreamFrameComplete) {
		t.Fatalf("frames = %#v, want chunk + completion", frames)
	}
}

func TestHTTPStreamDefaultsUnknownClientsToRawLegacyChunks(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "http-legacy", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"error": "service data", "$proxyma_stream": "complete"}
		return nil, nil
	})
	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "http-legacy"),
		strings.NewReader(`{}`),
	)
	rec := httptest.NewRecorder()
	srv.HandleServicesStream(rec, req)

	if selected := rec.Header().Get(protocol.HeaderStreamSelectedVersion); selected != protocol.StreamVersionLegacy {
		t.Fatalf("selected stream version = %q, want legacy", selected)
	}
	var frames []map[string]any
	if err := utils.ForEachNDJSON(rec.Body, func(frame map[string]any) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("decode legacy response: %v", err)
	}
	if len(frames) != 1 || frames[0]["error"] != "service data" {
		t.Fatalf("legacy frames = %#v, want one raw service chunk", frames)
	}
}

func TestHTTPV1StreamSupportsLargeImageFrame(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	image := strings.Repeat("a", 8<<20)
	registerContractStream(t, srv, "http-large-frame", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"image_base64": image}
		return nil, nil
	})
	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "http-large-frame"),
		strings.NewReader(`{}`),
	)
	req.Header.Set(protocol.HeaderStreamAcceptVersions, "1")
	rec := httptest.NewRecorder()
	srv.HandleServicesStream(rec, req)

	var imageLength int
	if err := utils.ForEachNDJSON(rec.Body, func(frame map[string]any) error {
		if frame["kind"] == string(protocol.ServiceStreamFrameChunk) {
			chunk := frame["chunk"].(map[string]any)
			imageLength = len(chunk["image_base64"].(string))
		}
		return nil
	}); err != nil {
		t.Fatalf("decode large v1 frame: %v", err)
	}
	if imageLength != len(image) {
		t.Fatalf("large image length = %d, want %d", imageLength, len(image))
	}
}

func TestHTTPStreamRejectsNonObjectPayloadBeforeExecution(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	var executions int
	registerContractStream(t, srv, "strict-http-payload", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		executions++
		return nil, nil
	})

	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "strict-http-payload"),
		strings.NewReader(`[]`),
	)
	rec := httptest.NewRecorder()
	srv.HandleServicesStream(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if executions != 0 {
		t.Fatalf("stream handler executed %d time(s)", executions)
	}
}

func TestLocalStreamRejectsNilChunkAsNonObject(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "nil-chunk", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- nil
		return nil, nil
	})

	called := false
	err := srv.LocalServiceStreamRun("nil-chunk", `{}`, func(map[string]any) {
		called = true
	})
	if err == nil {
		t.Fatal("local stream accepted a nil/non-object chunk")
	}
	if called {
		t.Fatal("nil stream chunk reached success callback")
	}
}

func TestLocalStreamReturnsHandlerErrorWithoutRequiringOutputClose(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "unclosed-error", func(
		context.Context,
		<-chan map[string]any,
		chan<- map[string]any,
		map[string]any,
	) (map[string]any, error) {
		return nil, errors.New("handler failed before closing output")
	})

	done := make(chan error, 1)
	go func() {
		done <- srv.LocalServiceStreamRun("unclosed-error", `{}`, nil)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "handler failed") {
			t.Fatalf("stream error = %v, want handler failure", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		srv.cancelLife()
		<-done
		t.Fatal("stream waited for output close after handler returned an error")
	}
}

func TestRemoteStreamReturnsTerminalAndMalformedNDJSONErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "terminal error",
			body: serviceStreamFrameLine(t, protocol.NewServiceStreamChunk(map[string]any{"n": 1})) +
				serviceStreamFrameLine(t, protocol.NewServiceStreamTerminal(
					protocol.ServiceStreamFrameError,
					"remote exploded",
				)),
		},
		{
			name: "malformed NDJSON",
			body: serviceStreamFrameLine(t, protocol.NewServiceStreamChunk(map[string]any{"n": 1})) +
				"not-json\n",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &streamContractPeerClient{MockPeerClient: &testutil.MockPeerClient{}}
			client.OnFetchServiceBid = func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
				return protocol.ServiceBid{
					NodeID:    "remote",
					CanAccept: true,
					Schema: protocol.ServiceSchema{
						Name: "remote-stream",
						Type: protocol.ServiceTypeBidi,
					},
				}, nil
			}
			client.stream = func(context.Context, string, string, map[string]any) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(tc.body)), nil
			}

			srv := newStreamContractServer(t, client)
			_, _ = srv.Peers.AddPeer("remote", protocol.AddressRecord{
				Addresses: []string{"https://remote.invalid"},
			})
			srv.Peers.SetPeerOnline("remote", true)

			var chunks []map[string]any
			err := srv.LocalServiceStreamRun("remote-stream", `{"input":"x"}`, func(chunk map[string]any) {
				chunks = append(chunks, chunk)
			})
			if err == nil {
				t.Fatalf("remote stream accepted %s", tc.name)
			}
			if len(chunks) != 1 || chunks[0]["n"] != float64(1) {
				t.Fatalf("chunks = %#v, want only valid service chunk", chunks)
			}
		})
	}
}

func TestRemoteStreamCompletesOnlyOnExplicitTerminalFrame(t *testing.T) {
	t.Parallel()

	client := &streamContractPeerClient{MockPeerClient: &testutil.MockPeerClient{}}
	client.OnFetchServiceBid = func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
		return protocol.ServiceBid{
			NodeID:    "remote",
			CanAccept: true,
			Schema: protocol.ServiceSchema{
				Name: "remote-success",
				Type: protocol.ServiceTypeBidi,
			},
		}, nil
	}
	client.stream = func(context.Context, string, string, map[string]any) (io.ReadCloser, error) {
		body := serviceStreamFrameLine(t, protocol.NewServiceStreamChunk(map[string]any{"n": 1})) +
			serviceStreamFrameLine(t, protocol.NewServiceStreamTerminal(
				protocol.ServiceStreamFrameComplete,
				"",
			))
		return io.NopCloser(strings.NewReader(body)), nil
	}
	srv := newStreamContractServer(t, client)
	_, _ = srv.Peers.AddPeer("remote", protocol.AddressRecord{
		Addresses: []string{"https://remote.invalid"},
	})
	srv.Peers.SetPeerOnline("remote", true)

	var chunks []map[string]any
	err := srv.LocalServiceStreamRun("remote-success", `{}`, func(chunk map[string]any) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("remote stream: %v", err)
	}
	if len(chunks) != 1 || chunks[0]["n"] != float64(1) {
		t.Fatalf("chunks = %#v, want one service chunk", chunks)
	}
}

func TestRemoteStreamRejectsMixedOrUnsupportedFraming(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name: "legacy then versioned",
			body: "{\"n\":1}\n" +
				serviceStreamFrameLine(t, protocol.NewServiceStreamTerminal(
					protocol.ServiceStreamFrameComplete,
					"",
				)),
			wantError: "negotiated v1 remote service stream emitted a legacy frame",
		},
		{
			name: "versioned then legacy",
			body: serviceStreamFrameLine(t, protocol.NewServiceStreamChunk(map[string]any{"n": 1})) +
				"{\"n\":2}\n",
			wantError: "negotiated v1 remote service stream emitted a legacy frame",
		},
		{
			name:      "unsupported version",
			body:      "{\"proxyma_stream_version\":2,\"kind\":\"complete\"}\n",
			wantError: "unsupported remote service stream version",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &streamContractPeerClient{MockPeerClient: &testutil.MockPeerClient{}}
			client.OnFetchServiceBid = func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
				return protocol.ServiceBid{
					NodeID:    "remote",
					CanAccept: true,
					Schema: protocol.ServiceSchema{
						Name: "remote-framing",
						Type: protocol.ServiceTypeBidi,
					},
				}, nil
			}
			client.stream = func(context.Context, string, string, map[string]any) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(test.body)), nil
			}
			srv := newStreamContractServer(t, client)
			_, _ = srv.Peers.AddPeer("remote", protocol.AddressRecord{
				Addresses: []string{"https://remote.invalid"},
			})
			srv.Peers.SetPeerOnline("remote", true)

			err := srv.LocalServiceStreamRun("remote-framing", `{}`, nil)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("stream error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestRemoteLegacyStreamTreatsAllObjectsAsChunksAndEOFSucceeds(t *testing.T) {
	t.Parallel()

	legacyVersion := 0
	body := "{\"error\":\"service data\"}\n" +
		"{\"$proxyma_stream\":\"complete\"}\n" +
		"{\"proxyma_stream_version\":1,\"kind\":\"error\",\"error\":\"also data\"}\n"
	client := &streamContractPeerClient{
		MockPeerClient:    &testutil.MockPeerClient{},
		negotiatedVersion: &legacyVersion,
	}
	client.OnFetchServiceBid = func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
		return protocol.ServiceBid{
			NodeID:    "remote",
			CanAccept: true,
			Schema: protocol.ServiceSchema{
				Name: "legacy-stream",
				Type: protocol.ServiceTypeBidi,
			},
		}, nil
	}
	client.stream = func(context.Context, string, string, map[string]any) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	srv := newStreamContractServer(t, client)
	_, _ = srv.Peers.AddPeer("remote", protocol.AddressRecord{
		Addresses: []string{"https://remote.invalid"},
	})
	srv.Peers.SetPeerOnline("remote", true)

	var chunks []map[string]any
	err := srv.LocalServiceStreamRun("legacy-stream", `{}`, func(chunk map[string]any) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("legacy EOF stream: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("legacy chunks = %#v, want all three data objects", chunks)
	}
}

func TestRemoteStreamHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	streamStarted := make(chan struct{})
	streamCanceled := make(chan struct{})
	client := &streamContractPeerClient{MockPeerClient: &testutil.MockPeerClient{}}
	client.OnFetchServiceBid = func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
		return protocol.ServiceBid{
			NodeID:    "remote",
			CanAccept: true,
			Schema: protocol.ServiceSchema{
				Name: "remote-cancel",
				Type: protocol.ServiceTypeBidi,
			},
		}, nil
	}
	client.stream = func(ctx context.Context, _ string, _ string, _ map[string]any) (io.ReadCloser, error) {
		close(streamStarted)
		<-ctx.Done()
		close(streamCanceled)
		return nil, ctx.Err()
	}
	srv := newStreamContractServer(t, client)
	_, _ = srv.Peers.AddPeer("remote", protocol.AddressRecord{
		Addresses: []string{"https://remote.invalid"},
	})
	srv.Peers.SetPeerOnline("remote", true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.LocalServiceStreamRunContext(ctx, "remote-cancel", `{}`, nil)
	}()
	waitServerLifecycleSignal(t, streamStarted, "remote stream start")
	cancel()
	waitServerLifecycleSignal(t, streamCanceled, "remote stream cancellation")
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream error = %v, want context.Canceled", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("remote stream did not return after caller cancellation")
	}
}

func TestLocalStreamHonorsServerShutdownCancellation(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	registerContractStream(t, srv, "shutdown-cancel", func(
		ctx context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		close(started)
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-release:
			return nil, nil
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- srv.LocalServiceStreamRun("shutdown-cancel", `{}`, nil)
	}()
	waitServerLifecycleSignal(t, started, "local stream start")
	srv.cancelLife()
	select {
	case <-canceled:
	case <-time.After(serverLifecycleTestTimeout):
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("server lifetime cancellation did not reach local stream")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream error = %v, want context.Canceled", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("local stream did not return after shutdown cancellation")
	}
}

func TestHTTPStreamCancellationReachesLocalHandler(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	registerContractStream(t, srv, "cancel-local", func(
		ctx context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		close(started)
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-release:
			return nil, nil
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "cancel-local"),
		strings.NewReader(`{}`),
	).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		srv.HandleServicesStream(httptest.NewRecorder(), req)
		close(done)
	}()

	waitServerLifecycleSignal(t, started, "stream handler start")
	cancel()
	select {
	case <-canceled:
	case <-time.After(serverLifecycleTestTimeout):
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("request cancellation did not reach local stream handler")
	}
	select {
	case <-done:
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("HTTP stream handler did not return after cancellation")
	}
}

type failingStreamResponseWriter struct {
	header http.Header
	err    error
}

func (w *failingStreamResponseWriter) Header() http.Header {
	return w.header
}

func (*failingStreamResponseWriter) WriteHeader(int) {}

func (w *failingStreamResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func (*failingStreamResponseWriter) Flush() {}

func TestHTTPStreamWriteFailureCancelsWork(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	registerContractStream(t, srv, "write-failure", func(
		ctx context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"n": 1}
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-release:
			return nil, nil
		}
	})

	req := httptest.NewRequest(
		http.MethodPost,
		protocol.WithServiceQuery(protocol.PathServicesStream, "write-failure"),
		strings.NewReader(`{}`),
	)
	done := make(chan struct{})
	go func() {
		srv.HandleServicesStream(&failingStreamResponseWriter{
			header: make(http.Header),
			err:    errors.New("client disconnected"),
		}, req)
		close(done)
	}()

	select {
	case <-canceled:
	case <-time.After(serverLifecycleTestTimeout):
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("response write failure did not cancel stream work")
	}
	select {
	case <-done:
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("HTTP stream handler did not return after write failure")
	}
}

func TestUnixStreamEmitsExplicitCompletionFrame(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "unix-success", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"n": 1}
		return nil, nil
	})

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go srv.handleUnixConnection(serverConn)
	requestBytes, err := json.Marshal(protocol.UnixRequest{
		Action:         "service_stream",
		StreamVersions: []int{protocol.ServiceStreamVersion},
		Args: map[string]string{
			"service": "unix-success",
			"payload": `{}`,
		},
	})
	if err != nil {
		t.Fatalf("marshal Unix request: %v", err)
	}
	if _, err := clientConn.Write(requestBytes); err != nil {
		t.Fatalf("write Unix request: %v", err)
	}

	var frames []map[string]any
	if err := utils.ForEachNDJSON(clientConn, func(frame map[string]any) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("scan Unix stream: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("Unix frames = %#v, want chunk + completion", frames)
	}
	if complete, _ := frames[1]["complete"].(bool); !complete {
		t.Fatalf("last Unix frame = %#v, want explicit completion", frames[1])
	}
	if version := frames[1]["stream_version"]; version != float64(protocol.ServiceStreamVersion) {
		t.Fatalf("last Unix frame version = %#v, want v1", version)
	}
}

func TestUnixStreamDefaultsUnknownClientsToLegacyEOF(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	registerContractStream(t, srv, "unix-legacy", func(
		_ context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		out <- map[string]any{"error": "service data"}
		return nil, nil
	})

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go srv.handleUnixConnection(serverConn)
	requestBytes, err := json.Marshal(protocol.UnixRequest{
		Action: "service_stream",
		Args: map[string]string{
			"service": "unix-legacy",
			"payload": `{}`,
		},
	})
	if err != nil {
		t.Fatalf("marshal Unix request: %v", err)
	}
	if _, err := clientConn.Write(requestBytes); err != nil {
		t.Fatalf("write Unix request: %v", err)
	}

	var frames []map[string]any
	if err := utils.ForEachNDJSON(clientConn, func(frame map[string]any) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("scan legacy Unix stream: %v", err)
	}
	if len(frames) != 1 || frames[0]["complete"] != nil || frames[0]["stream_version"] != nil {
		t.Fatalf("legacy Unix frames = %#v, want one unversioned chunk and EOF", frames)
	}
}

func TestUnixStreamDisconnectCancelsHandler(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	registerContractStream(t, srv, "unix-cancel", func(
		ctx context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		close(started)
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-release:
			return nil, nil
		}
	})

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleUnixConnection(serverConn)
		close(done)
	}()
	requestBytes, err := json.Marshal(protocol.UnixRequest{
		Action: "service_stream",
		Args: map[string]string{
			"service": "unix-cancel",
			"payload": `{}`,
		},
	})
	if err != nil {
		t.Fatalf("marshal Unix request: %v", err)
	}
	if _, err := clientConn.Write(requestBytes); err != nil {
		t.Fatalf("write Unix request: %v", err)
	}
	waitServerLifecycleSignal(t, started, "Unix stream handler start")
	if err := clientConn.Close(); err != nil {
		t.Fatalf("close Unix client: %v", err)
	}

	select {
	case <-canceled:
	case <-time.After(serverLifecycleTestTimeout):
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("Unix client disconnect did not cancel stream handler")
	}
	select {
	case <-done:
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("Unix connection handler did not exit after disconnect")
	}
}

func TestServiceNotifyReturnsServerErrorOnSubscriptionReadFailure(t *testing.T) {
	t.Parallel()

	srv := newStreamContractServer(t, &testutil.MockPeerClient{})
	if err := srv.Storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}
	body, err := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer",
		Schema: protocol.ServiceSchema{Name: "strict-subscription"},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: "peer"},
		}},
	}
	rec := httptest.NewRecorder()

	srv.HandleServiceNotify(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
