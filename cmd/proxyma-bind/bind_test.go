package proxyma_bind

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNodeLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// Ensure no node is running initially
	StopNode()
	assert.False(t, IsNodeRunning())
	assert.Empty(t, GetNodeID())
	assert.Empty(t, GetNodeAddress())

	// Start the node in a temporary directory
	errStr := StartNode(tempDir, true)
	assert.Empty(t, errStr, "StartNode should succeed and return empty string")

	assert.True(t, IsNodeRunning())
	assert.NotEmpty(t, GetNodeID())
	assert.Equal(t, "https://127.0.0.1:8080", GetNodeAddress())

	// Test bandwidth stats functions (should be 0 or populated)
	assert.Zero(t, GetUploadSpeed())
	assert.Zero(t, GetDownloadSpeed())
	assert.Zero(t, GetTotalSent())
	assert.Zero(t, GetTotalReceived())

	// Verify storage path helper
	assert.Equal(t, tempDir, GetStoragePath())

	// Wait briefly for goroutines
	time.Sleep(100 * time.Millisecond)

	// Clean stop
	StopNode()
	assert.False(t, IsNodeRunning())
	assert.Empty(t, GetNodeID())
	assert.Empty(t, GetNodeAddress())

	// Cleanup remaining mock structures if any
	_ = os.RemoveAll(tempDir)
}

type mockStreamListener struct {
	chunks []string
	done   chan struct{}
	err    string
}

func (m *mockStreamListener) OnChunk(chunkJSON string) {
	m.chunks = append(m.chunks, chunkJSON)
}

func (m *mockStreamListener) OnError(errMsg string) {
	m.err = errMsg
	close(m.done)
}

func (m *mockStreamListener) OnComplete() {
	close(m.done)
}

func TestStreamService_ValidationError(t *testing.T) {
	res := StreamService("", `{}`, nil)
	if !IsBindError(res) {
		t.Fatalf("expected bind error for empty name, got %s", res)
	}
}

func TestStreamService_GomobileBinding(t *testing.T) {
	tempDir := t.TempDir()
	StartNode(tempDir, true)
	defer StopNode()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		_ = encoder.Encode(map[string]any{"chunk": 1})
		_ = encoder.Encode(map[string]any{"chunk": 2})
	}))
	defer ts.Close()

	AddService("bidi_test", "grpc_bidi", ts.URL, "bidi service test", "", "", "")

	if s := getSrv(); s != nil {
		s.LoadLocalServices()
	}

	listener := &mockStreamListener{done: make(chan struct{})}
	res := StreamService("bidi_test", `{"input":"go"}`, listener)
	assert.Contains(t, res, "streaming_started")

	select {
	case <-listener.done:
		assert.Empty(t, listener.err)
		assert.Len(t, listener.chunks, 2)
	case <-time.After(3 * time.Second):
		t.Fatal("StreamService listener timed out")
	}
}
