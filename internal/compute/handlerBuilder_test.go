package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
