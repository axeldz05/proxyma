package compute

import (
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

func TestBuildGRPCBidiStreamHandler_Success(t *testing.T) {

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/x-ndjson", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		decoder := json.NewDecoder(r.Body)
		encoder := json.NewEncoder(w)

		for {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}

			// Echo modified payload back
			msg["processed"] = true
			err := encoder.Encode(msg)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiStreamHandler(ts.URL, 5*time.Second)

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

	res1, ok1 := <-out
	require.True(t, ok1)
	require.Equal(t, "audio_frame_1", res1["task"])
	require.Equal(t, true, res1["processed"])

	res2, ok2 := <-out
	require.True(t, ok2)
	require.Equal(t, "audio_frame_2", res2["task"])
	require.Equal(t, true, res2["processed"])

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

func TestBuildGRPCBidiStreamHandler_ContextCancellation(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiStreamHandler(ts.URL, 0)

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

func TestBuildGRPCBidiStreamHandler_ServerError(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	handler := BuildGRPCBidiStreamHandler(ts.URL, 5*time.Second)

	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 1)

	in <- map[string]any{"test": "val"}
	close(in)

	err := handler.ExecuteStream(context.Background(), in, out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}
