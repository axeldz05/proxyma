package proxyma_bind

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyma/internal/protocol"
)

type terminalRecordingListener struct {
	mu        sync.Mutex
	err       string
	completed bool
	done      chan struct{}
	once      sync.Once
}

func (l *terminalRecordingListener) OnChunk(string) {}

func (l *terminalRecordingListener) OnError(message string) {
	l.mu.Lock()
	l.err = message
	l.mu.Unlock()
	l.once.Do(func() { close(l.done) })
}

func (l *terminalRecordingListener) OnComplete() {
	l.mu.Lock()
	l.completed = true
	l.mu.Unlock()
	l.once.Do(func() { close(l.done) })
}

func TestUnixLegacyStreamEOFTerminatesSuccessfully(t *testing.T) {
	StopNode()
	tempDir := t.TempDir()
	if err := protocol.SaveConfig(protocol.NodeConfig{
		ID:          "unix-stream-contract",
		StoragePath: tempDir,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	SetStoragePath(tempDir)

	listener, err := net.Listen("unix", filepath.Join(tempDir, protocol.SockFileName))
	if err != nil {
		t.Fatalf("listen Unix socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan struct{})
	requestSeen := make(chan protocol.UnixRequest, 1)
	go func() {
		defer close(serverDone)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req protocol.UnixRequest
		if json.NewDecoder(conn).Decode(&req) != nil {
			return
		}
		requestSeen <- req
		_, _ = conn.Write([]byte("{\"success\":true,\"data\":{\"n\":1}}\n"))
	}()

	recording := &terminalRecordingListener{done: make(chan struct{})}
	result := StreamService("remote-stream", `{}`, recording)
	if IsBindError(result) {
		t.Fatalf("start stream: %s", result)
	}
	select {
	case <-recording.done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream listener did not receive terminal callback")
	}
	<-serverDone
	request := <-requestSeen
	if len(request.StreamVersions) != 1 || request.StreamVersions[0] != protocol.ServiceStreamVersion {
		t.Fatalf("advertised Unix stream versions = %#v, want v1", request.StreamVersions)
	}

	recording.mu.Lock()
	defer recording.mu.Unlock()
	if recording.err != "" {
		t.Fatalf("legacy EOF reported error %q", recording.err)
	}
	if !recording.completed {
		t.Fatal("legacy EOF did not call OnComplete")
	}
}

func TestStreamServiceRejectsMalformedAndNonObjectPayloadSynchronously(t *testing.T) {
	StopNode()

	for _, payload := range []string{`{bad`, `[]`, `null`, `"text"`} {
		result := StreamService("strict-stream", payload, nil)
		if !IsBindError(result) {
			t.Errorf("StreamService payload %q result = %s, want bind error", payload, result)
			continue
		}
		if message := ParseBindError(result); !strings.Contains(message, "invalid service payload") {
			t.Errorf("StreamService payload %q error = %q", payload, message)
		}
	}
}

func TestRunServiceRejectsMalformedAndNonObjectPayloadSynchronously(t *testing.T) {
	StopNode()

	for _, payload := range []string{`{bad`, `[]`, `null`, `"text"`} {
		result := RunServiceWithStrategy("strict-unary", payload, "")
		if !IsBindError(result) {
			t.Errorf("RunService payload %q result = %s, want bind error", payload, result)
			continue
		}
		if message := ParseBindError(result); !strings.Contains(message, "invalid service payload") {
			t.Errorf("RunService payload %q error = %q", payload, message)
		}
	}
}

func TestStreamServiceCanBeCanceledByReturnedStreamID(t *testing.T) {
	StopNode()
	tempDir := t.TempDir()
	StartNode(tempDir, true)
	t.Cleanup(StopNode)

	srv := getSrv()
	if srv == nil {
		t.Fatal("local server did not start")
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "cancelable-bind-stream",
		Type: protocol.ServiceTypeBidi,
	}, func(
		ctx context.Context,
		_ <-chan map[string]any,
		out chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		defer close(out)
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}); err != nil {
		t.Fatalf("register cancelable stream: %v", err)
	}

	recording := &terminalRecordingListener{done: make(chan struct{})}
	result := StreamService("cancelable-bind-stream", `{}`, recording)
	if IsBindError(result) {
		t.Fatalf("start stream: %s", result)
	}
	var startedResponse struct {
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal([]byte(result), &startedResponse); err != nil || startedResponse.StreamID == "" {
		t.Fatalf("stream start response = %q, error = %v", result, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("local bind stream did not start")
	}

	if response := CancelStream(startedResponse.StreamID); IsBindError(response) {
		t.Fatalf("cancel stream: %s", response)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("bind cancellation did not reach local handler")
	}
	select {
	case <-recording.done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled bind stream did not terminate listener")
	}
	recording.mu.Lock()
	if recording.completed || !strings.Contains(recording.err, context.Canceled.Error()) {
		t.Fatalf("listener completed=%v error=%q, want cancellation error", recording.completed, recording.err)
	}
	recording.mu.Unlock()

	if response := CancelStream(startedResponse.StreamID); IsBindError(response) {
		t.Fatalf("repeated cancellation should be idempotent: %s", response)
	}
}

func TestRunServiceKeepsTwoArgumentABI(t *testing.T) {
	var run = RunService
	if result := run("strict-unary", `[]`); !IsBindError(result) {
		t.Fatalf("two-argument RunService result = %s, want validation error", result)
	}
	if result := RunServiceWithStrategy("strict-unary", `[]`, "fastest"); !IsBindError(result) {
		t.Fatalf("RunServiceWithStrategy result = %s, want validation error", result)
	}
}
