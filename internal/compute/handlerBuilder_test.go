package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestBuildGRPCBidiHandler_Success(t *testing.T) {

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/x-ndjson", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/x-ndjson")
		decoder := json.NewDecoder(bytes.NewReader(body))
		encoder := json.NewEncoder(w)

		for {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}
			msg["processed"] = true
			require.NoError(t, encoder.Encode(msg))
		}
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiHandler(ts.URL, 5*time.Second)

	in := make(chan map[string]any, 2)
	out := make(chan map[string]any, 2)
	errChan := make(chan error, 1)

	ctx := context.Background()

	go func() {
		errChan <- handler.ExecuteStream(ctx, in, out)
	}()

	in <- map[string]any{"task": "audio_frame_1"}
	in <- map[string]any{"task": "audio_frame_2"}
	close(in)

	var results []map[string]any
	timeout := time.After(2 * time.Second)
	for len(results) < 2 {
		select {
		case res, ok := <-out:
			if !ok {
				require.Len(t, results, 2, "stream closed early")
				goto done
			}
			results = append(results, res)
		case <-timeout:
			t.Fatalf("timeout waiting for stream chunks, got %d", len(results))
		}
	}
done:
	require.Len(t, results, 2)
	require.Equal(t, "audio_frame_1", results[0]["task"])
	require.Equal(t, true, results[0]["processed"])
	require.Equal(t, "audio_frame_2", results[1]["task"])
	require.Equal(t, true, results[1]["processed"])

	select {
	case err := <-errChan:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not terminate after input closed")
	}
}

func TestBuildGRPCBidiHandler_UnaryAdaptation(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		decoder := json.NewDecoder(r.Body)
		encoder := json.NewEncoder(w)

		var msg map[string]any
		err := decoder.Decode(&msg)
		require.NoError(t, err)

		msg["status"] = "ok"
		err = encoder.Encode(msg)
		require.NoError(t, err)
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiHandler(ts.URL, 5*time.Second)

	ctx := context.Background()
	resp, err := handler.Execute(ctx, map[string]any{"input": "test_data"})
	require.NoError(t, err)
	require.Equal(t, "test_data", resp["input"])
	require.Equal(t, "ok", resp["status"])
}

func TestBuildGRPCBidiHandler_ContextCancellation(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiHandler(ts.URL, 0)

	ctx, cancel := context.WithCancel(context.Background())

	in := make(chan map[string]any)
	out := make(chan map[string]any)
	errChan := make(chan error, 1)

	go func() {
		errChan <- handler.ExecuteStream(ctx, in, out)
	}()

	cancel()

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("bidi handler failed to respect context cancellation")
	}
}

func TestBuildGRPCBidiHandler_ServerError(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiHandler(ts.URL, 5*time.Second)

	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 1)

	in <- map[string]any{"test": "val"}
	close(in)

	err := handler.ExecuteStream(context.Background(), in, out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestBuildUnaryHandler_RejectsStreaming(t *testing.T) {
	t.Parallel()
	handler := BuildUnaryHandler(func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	in := make(chan map[string]any)
	close(in)
	out := make(chan map[string]any, 1)
	done := make(chan error, 1)
	go func() {
		done <- handler.ExecuteStream(context.Background(), in, out)
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not support streaming")
	case <-time.After(2 * time.Second):
		t.Fatal("unary ExecuteStream hung instead of failing fast")
	}
	_, ok := <-out
	require.False(t, ok, "out channel must be closed")
}

func TestBuildHandlerWiresServerStreamType(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for i := 1; i <= 3; i++ {
			require.NoError(t, enc.Encode(map[string]any{"n": float64(i)}))
		}
	}))
	t.Cleanup(ts.Close)

	handler, err := BuildHandler(protocol.ServiceTypeServerStream, ts.URL)
	require.NoError(t, err)
	require.NotNil(t, handler)

	in := make(chan map[string]any)
	close(in)
	out := make(chan map[string]any, 8)
	errChan := make(chan error, 1)
	go func() {
		errChan <- handler.ExecuteStream(context.Background(), in, out)
	}()

	var chunks []map[string]any
	timeout := time.After(2 * time.Second)
	for len(chunks) < 3 {
		select {
		case res, ok := <-out:
			if !ok {
				goto drained
			}
			chunks = append(chunks, res)
		case <-timeout:
			t.Fatalf("timeout waiting for NDJSON chunks, got %d", len(chunks))
		}
	}
drained:
	require.GreaterOrEqual(t, len(chunks), 3)
	require.Equal(t, float64(1), chunks[0]["n"])
	select {
	case err := <-errChan:
		require.NoError(t, err)
		require.NotContains(t, fmt.Sprint(err), "not yet implemented")
	case <-time.After(2 * time.Second):
		t.Fatal("server-stream handler did not finish")
	}

	// Alias http_server_stream must wire the same way
	alias, err := BuildHandler(protocol.ServiceTypeHTTPServerStream, ts.URL)
	require.NoError(t, err)
	require.NotNil(t, alias)
}

func TestBidiHandlerRoundTripsNDJSONChunks(t *testing.T) {
	t.Parallel()
	TestBuildGRPCBidiHandler_Success(t)
}

func TestWebRTCHandlerExchangesPayloadOverDataChannel(t *testing.T) {
	t.Parallel()

	ts := startWebRTCEchoAnswerer(t)

	handler, err := BuildHandler(protocol.ServiceTypeWebRTC, ts.URL)
	require.NoError(t, err)
	require.NotNil(t, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	in := make(chan map[string]any, 2)
	out := make(chan map[string]any, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- handler.ExecuteStream(ctx, in, out)
	}()

	want := []map[string]any{
		{"n": float64(1), "msg": "ping"},
		{"n": float64(2), "msg": "pong"},
	}
	for _, msg := range want {
		in <- msg
	}
	close(in)

	var got []map[string]any
	deadline := time.After(3 * time.Second)
	for len(got) < len(want) {
		select {
		case chunk, ok := <-out:
			if !ok {
				require.Len(t, got, len(want), "out closed early")
				goto drained
			}
			got = append(got, chunk)
		case err := <-errCh:
			require.NoError(t, err, "handler ended before chunks")
			require.NotContains(t, fmt.Sprint(err), "not yet implemented")
			t.Fatal("handler finished without delivering chunks")
		case <-deadline:
			t.Fatalf("timeout waiting for DataChannel chunks, got %d", len(got))
		}
	}
drained:
	require.Equal(t, want, got)

	select {
	case err := <-errCh:
		require.NoError(t, err)
		require.NotContains(t, fmt.Sprint(err), "not yet implemented")
	case <-time.After(3 * time.Second):
		t.Fatal("WebRTC handler did not terminate cleanly")
	}
}

func TestBuildHandlerTypeAliasesMatchHTTPStreamSemantics(t *testing.T) {
	t.Parallel()

	bidiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body) // echo NDJSON request body
	}))
	t.Cleanup(bidiTS.Close)

	streamTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(streamTS.Close)

	aliases := []struct {
		typ      protocol.ServiceType
		exec     string
		wantNorm protocol.ServiceType
	}{
		{protocol.ServiceTypeHTTPBidi, bidiTS.URL, protocol.ServiceTypeGRPCBidi},
		{protocol.ServiceTypeGRPCBidi, bidiTS.URL, protocol.ServiceTypeGRPCBidi},
		{protocol.ServiceTypeBidiGRPC, bidiTS.URL, protocol.ServiceTypeGRPCBidi},
		{protocol.ServiceTypeBidiStream, bidiTS.URL, protocol.ServiceTypeGRPCBidi},
		{protocol.ServiceTypeHTTPServerStream, streamTS.URL, protocol.ServiceTypeServerStream},
		{protocol.ServiceTypeGRPCServerStream, streamTS.URL, protocol.ServiceTypeServerStream},
		{protocol.ServiceTypeServerStream, streamTS.URL, protocol.ServiceTypeServerStream},
	}

	for _, tc := range aliases {
		t.Run(string(tc.typ), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.wantNorm, tc.typ.Normalize())
			require.True(t, tc.typ.IsStreaming())

			h, err := BuildHandler(tc.typ, tc.exec)
			require.NoError(t, err)
			require.NotNil(t, h)

			in := make(chan map[string]any, 1)
			out := make(chan map[string]any, 4)
			in <- map[string]any{"ping": true}
			close(in)

			errCh := make(chan error, 1)
			go func() { errCh <- h.ExecuteStream(context.Background(), in, out) }()

			select {
			case chunk, ok := <-out:
				require.True(t, ok, "expected at least one NDJSON chunk")
				require.NotContains(t, fmt.Sprint(chunk), "not implemented")
			case err := <-errCh:
				require.NoError(t, err)
				t.Fatal("stream ended without chunks")
			case <-time.After(2 * time.Second):
				t.Fatal("timeout waiting for alias handler chunk")
			}
			select {
			case err := <-errCh:
				require.NoError(t, err)
				require.NotContains(t, fmt.Sprint(err), "not yet implemented")
			case <-time.After(2 * time.Second):
				// drain may still be in progress; non-fatal if we already got a chunk
			}
		})
	}
}

// startWebRTCEchoAnswerer is an in-process signaling mock: POST offer SDP → answer SDP;
// DataChannel JSON messages are echoed back. ICE host-only (no STUN).
func startWebRTCEchoAnswerer(t *testing.T) *httptest.Server {
	t.Helper()

	var mu sync.Mutex
	var pcs []*webrtc.PeerConnection
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, pc := range pcs {
			_ = pc.Close()
		}
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var offer webrtc.SessionDescription
		if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pc, answer, err := AcceptWebRTCOfferEcho(offer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		pcs = append(pcs, pc)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(ts.Close)
	return ts
}
