package proxyma_bind

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
)

const (
	liveDaemonEnv         = "PROXYMA_BIND_LIVE_DAEMON"
	liveDaemonStorageEnv  = "PROXYMA_BIND_LIVE_STORAGE"
	liveDaemonReadyMarker = "PROXYMA_BIND_LIVE_READY"
	liveDaemonStartMarker = "PROXYMA_BIND_LIVE_START_ERROR "
	liveDaemonStopMarker  = "PROXYMA_BIND_LIVE_STOP_RESULT "
)

// TestIntegrationLiveDaemonProcess is a subprocess fixture. Running the daemon
// in another process ensures the caller exercises the public Unix IPC path
// rather than the in-process dispatch shortcut.
func TestIntegrationLiveDaemonProcess(t *testing.T) {
	if os.Getenv(liveDaemonEnv) != "1" {
		t.Skip("subprocess fixture")
	}

	result := StartNode(os.Getenv(liveDaemonStorageEnv), false)
	if result != "" {
		fmt.Println(liveDaemonStartMarker + base64.StdEncoding.EncodeToString([]byte(result)))
		t.Fail()
		return
	}
	fmt.Println(liveDaemonReadyMarker)

	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	stopResult := StopNodeWithError()
	fmt.Println(liveDaemonStopMarker + base64.StdEncoding.EncodeToString([]byte(stopResult)))
	if stopResult != "" {
		t.Errorf("StopNodeWithError: %s", stopResult)
	}
}

type liveDaemonOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *liveDaemonOutput) add(line string) {
	o.mu.Lock()
	o.lines = append(o.lines, line)
	o.mu.Unlock()
}

func (o *liveDaemonOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.lines, "\n")
}

type liveBindDaemon struct {
	storage  string
	stdin    io.WriteCloser
	stopCh   <-chan string
	wait     *liveDaemonWait
	output   *liveDaemonOutput
	process  *os.Process
	stopOnce sync.Once
	stopErr  error
}

type liveDaemonWait struct {
	done chan struct{}
	err  error
}

func startLiveBindDaemon(t *testing.T, nodeID string) *liveBindDaemon {
	t.Helper()
	StopNode()

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		nodeID,
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatalf("set up live node: %v", err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestIntegrationLiveDaemonProcess$")
	cmd.Env = append(
		os.Environ(),
		liveDaemonEnv+"=1",
		liveDaemonStorageEnv+"="+storagePath,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("open daemon stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open daemon stdout: %v", err)
	}
	cmd.Stderr = cmd.Stdout

	readyCh := make(chan struct{}, 1)
	startErrCh := make(chan string, 1)
	stopCh := make(chan string, 1)
	output := &liveDaemonOutput{}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			switch {
			case line == liveDaemonReadyMarker:
				readyCh <- struct{}{}
			case strings.HasPrefix(line, liveDaemonStartMarker):
				startErrCh <- decodeLiveDaemonResult(strings.TrimPrefix(line, liveDaemonStartMarker))
			case strings.HasPrefix(line, liveDaemonStopMarker):
				stopCh <- decodeLiveDaemonResult(strings.TrimPrefix(line, liveDaemonStopMarker))
			}
		}
		if err := scanner.Err(); err != nil {
			output.add("scan daemon output: " + err.Error())
		}
	}()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start live daemon: %v", err)
	}
	wait := &liveDaemonWait{done: make(chan struct{})}
	go func() {
		wait.err = cmd.Wait()
		close(wait.done)
	}()

	daemon := &liveBindDaemon{
		storage: storagePath,
		stdin:   stdin,
		stopCh:  stopCh,
		wait:    wait,
		output:  output,
		process: cmd.Process,
	}
	t.Cleanup(func() {
		if err := daemon.stop(); err != nil {
			t.Errorf("stop live daemon: %v\n%s", err, daemon.output.String())
		}
	})

	select {
	case <-readyCh:
	case result := <-startErrCh:
		_ = daemon.stop()
		t.Fatalf("StartNode subprocess failed: %s\n%s", result, output.String())
	case <-wait.done:
		t.Fatalf("live daemon exited before readiness: %v\n%s", wait.err, output.String())
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("live daemon did not become ready\n%s", output.String())
	}

	SetStoragePath(storagePath)
	return daemon
}

func (d *liveBindDaemon) stop() error {
	d.stopOnce.Do(func() {
		if _, err := io.WriteString(d.stdin, "stop\n"); err != nil {
			d.stopErr = fmt.Errorf("request stop: %w", err)
			_ = d.stdin.Close()
			_ = d.process.Kill()
			<-d.wait.done
			return
		}
		_ = d.stdin.Close()

		select {
		case result := <-d.stopCh:
			if result != "" {
				d.stopErr = fmt.Errorf("StopNodeWithError returned %s", result)
			}
		case <-time.After(10 * time.Second):
			d.stopErr = fmt.Errorf("timed out waiting for StopNodeWithError")
			_ = d.process.Kill()
			<-d.wait.done
			return
		}
		select {
		case <-d.wait.done:
			if d.wait.err != nil && d.stopErr == nil {
				d.stopErr = fmt.Errorf("daemon process: %w", d.wait.err)
			}
		case <-time.After(5 * time.Second):
			if d.stopErr == nil {
				d.stopErr = fmt.Errorf("daemon process did not exit")
			}
			_ = d.process.Kill()
			<-d.wait.done
		}
	})
	return d.stopErr
}

func decodeLiveDaemonResult(encoded string) string {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "invalid fixture result: " + err.Error()
	}
	return string(decoded)
}

func TestLiveDaemonPublicActionContracts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		value, ok := payload["value"].(float64)
		if !ok {
			http.Error(w, "value must be numeric", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": value + 1})
	}))
	t.Cleanup(upstream.Close)

	daemon := startLiveBindDaemon(t, "live-actions")
	schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
		Name: "live.echo",
		Type: protocol.ServiceTypeGRPC,
		Parameters: map[string]protocol.ServiceParameter{
			"value": {Type: protocol.ParamTypeInt, Required: true},
		},
		Outputs: map[string]protocol.ServiceParameter{
			"result": {Type: protocol.ParamTypeInt},
		},
	})

	assertLiveBindSuccess(t, InvokeDomainAction("service", "add", map[string]string{
		"name":        "live.echo",
		"type":        string(protocol.ServiceTypeGRPC),
		"exec":        upstream.URL,
		"schema-file": schemaPath,
	}))

	var discovered []string
	decodeLiveBindJSON(t, InvokeDomainAction("service", "discover", nil), &discovered)
	if !containsString(discovered, "live.echo") {
		t.Fatalf("service discovery result = %#v, want live.echo", discovered)
	}

	var task protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, InvokeDomainAction("service", "run", map[string]string{
		"name":    "live.echo",
		"payload": `{"value":7}`,
	}), &task)
	if task.Status != "completed" || task.Outputs["result"] != float64(8) {
		t.Fatalf("service run response = %#v, want completed result 8", task)
	}

	inputPath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(inputPath, []byte("live storage contract"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertLiveBindSuccess(t, InvokeDomainAction("storage", "upload", map[string]string{
		"path": inputPath,
		"name": "contract.txt",
	}))

	var files []protocol.VFSFileStatus
	decodeLiveBindJSON(t, InvokeDomainAction("storage", "list", nil), &files)
	var uploaded *protocol.VFSFileStatus
	for i := range files {
		if files[i].Name == "contract.txt" {
			uploaded = &files[i]
			break
		}
	}
	if uploaded == nil || !uploaded.HasLocal || uploaded.Size != int64(len("live storage contract")) || uploaded.Hash == "" {
		t.Fatalf("storage list result = %#v, want local contract.txt metadata", files)
	}

	if err := daemon.stop(); err != nil {
		t.Fatalf("clean StopNodeWithError contract failed: %v\n%s", err, daemon.output.String())
	}
}

type liveStreamTerminal struct {
	err       string
	completed bool
}

type liveStreamListener struct {
	chunks   chan string
	terminal chan liveStreamTerminal
	once     sync.Once
}

func newLiveStreamListener() *liveStreamListener {
	return &liveStreamListener{
		chunks:   make(chan string, 8),
		terminal: make(chan liveStreamTerminal, 1),
	}
}

func (l *liveStreamListener) OnChunk(chunkJSON string) {
	l.chunks <- chunkJSON
}

func (l *liveStreamListener) OnError(errMsg string) {
	l.once.Do(func() { l.terminal <- liveStreamTerminal{err: errMsg} })
}

func (l *liveStreamListener) OnComplete() {
	l.once.Do(func() { l.terminal <- liveStreamTerminal{completed: true} })
}

func TestLiveDaemonStreamServiceContracts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		switch r.URL.Path {
		case "/complete":
			_ = encoder.Encode(map[string]any{"sequence": 1})
			_ = encoder.Encode(map[string]any{"sequence": 2})
		case "/cancel":
			_ = encoder.Encode(map[string]any{"phase": "started"})
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	startLiveBindDaemon(t, "live-stream")
	for _, service := range []struct {
		name string
		path string
	}{
		{name: "stream.complete", path: "/complete"},
		{name: "stream.cancel", path: "/cancel"},
	} {
		schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
			Name:       service.name,
			Type:       protocol.ServiceTypeServerStream,
			Parameters: map[string]protocol.ServiceParameter{},
		})
		assertLiveBindSuccess(t, InvokeDomainAction("service", "add", map[string]string{
			"name":        service.name,
			"type":        string(protocol.ServiceTypeServerStream),
			"exec":        upstream.URL + service.path,
			"schema-file": schemaPath,
		}))
	}

	completedListener := newLiveStreamListener()
	startResult := StreamService("stream.complete", `{}`, completedListener)
	assertLiveBindSuccess(t, startResult)
	var started struct {
		StreamID string `json:"stream_id"`
	}
	decodeLiveBindJSON(t, startResult, &started)
	if started.StreamID == "" {
		t.Fatalf("stream start response = %s, want stream_id", startResult)
	}
	terminal := awaitLiveStreamTerminal(t, completedListener)
	if !terminal.completed || terminal.err != "" {
		t.Fatalf("completed stream terminal = %#v", terminal)
	}
	first := awaitLiveStreamChunk(t, completedListener)
	second := awaitLiveStreamChunk(t, completedListener)
	if first["sequence"] != float64(1) || second["sequence"] != float64(2) {
		t.Fatalf("stream chunks = %#v then %#v", first, second)
	}

	canceledListener := newLiveStreamListener()
	cancelStart := StreamService("stream.cancel", `{}`, canceledListener)
	assertLiveBindSuccess(t, cancelStart)
	var cancelStarted struct {
		StreamID string `json:"stream_id"`
	}
	decodeLiveBindJSON(t, cancelStart, &cancelStarted)
	chunk := awaitLiveStreamChunk(t, canceledListener)
	if chunk["phase"] != "started" {
		t.Fatalf("cancelable stream first chunk = %#v", chunk)
	}
	assertLiveBindSuccess(t, CancelStream(cancelStarted.StreamID))
	canceledTerminal := awaitLiveStreamTerminal(t, canceledListener)
	if canceledTerminal.completed || !strings.Contains(strings.ToLower(canceledTerminal.err), "canceled") {
		t.Fatalf("canceled stream terminal = %#v, want cancellation error", canceledTerminal)
	}
	secondCancel := CancelStream(cancelStarted.StreamID)
	assertLiveBindSuccess(t, secondCancel)
	var secondCancelMessage protocol.APIMessage
	decodeLiveBindJSON(t, secondCancel, &secondCancelMessage)
	if !strings.Contains(strings.ToLower(secondCancelMessage.Message), "not running") {
		t.Fatalf("second CancelStream response = %#v, want idempotent not-running success", secondCancelMessage)
	}
}

func TestJoinClusterAgainstLiveSponsorContract(t *testing.T) {
	StopNode()
	sponsor := startLiveBindDaemon(t, "live-sponsor")
	t.Cleanup(StopNode)

	token := GenerateInviteToken()
	if IsBindError(token) {
		t.Fatalf("GenerateInviteToken: %s", token)
	}
	invite, secret, err := p2p.ParseSmartToken(token)
	if err != nil {
		t.Fatalf("parse generated invite: %v", err)
	}
	sponsorConfig, err := protocol.LoadConfig(sponsor.storage)
	if err != nil {
		t.Fatalf("load sponsor config: %v", err)
	}
	joinClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: p2p.TLSConfigTrustCAHash(invite.CAHash)},
		Timeout:   3 * time.Second,
	}

	invalidStatus := postLiveJoin(t, joinClient, sponsorConfig.Address, protocol.JoinRequest{
		Secret:  secret,
		CSR:     "not a certificate request",
		ID:      "live-joiner",
		Address: protocol.HTTPSAddr("127.0.0.1", "0"),
	})
	if invalidStatus != http.StatusBadRequest {
		t.Fatalf("invalid enrollment status = %d, want %d", invalidStatus, http.StatusBadRequest)
	}

	joinerStorage := t.TempDir()
	if result := JoinCluster(joinerStorage, token, "live-joiner", "0"); result != "" {
		t.Fatalf("JoinCluster after invalid enrollment: %s", result)
	}
	if !IsNodeRunning() || GetNodeID() != "live-joiner" {
		t.Fatalf("joined node status running=%v id=%q", IsNodeRunning(), GetNodeID())
	}

	replayCSR, _, err := p2p.GenerateNodeCSR("replay-node")
	if err != nil {
		t.Fatalf("generate replay CSR: %v", err)
	}
	replayStatus := postLiveJoin(t, joinClient, sponsorConfig.Address, protocol.JoinRequest{
		Secret:  secret,
		CSR:     string(replayCSR),
		ID:      "replay-node",
		Address: protocol.HTTPSAddr("127.0.0.1", "0"),
	})
	if replayStatus != http.StatusUnauthorized {
		t.Fatalf("replayed enrollment status = %d, want %d", replayStatus, http.StatusUnauthorized)
	}

	if result := StopNodeWithError(); result != "" {
		t.Fatalf("stop joined node: %s", result)
	}
}

func TestLiveDaemonPipelineContracts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/increment":
			value, ok := payload["seed"].(float64)
			if !ok {
				http.Error(w, "seed must be numeric", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": value + 1})
		case "/double":
			value, ok := payload["value"].(float64)
			if !ok {
				http.Error(w, "value must be numeric", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": value * 2})
		case "/string":
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": payload["text"]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	startLiveBindDaemon(t, "live-pipeline")
	services := []struct {
		name   string
		path   string
		input  map[string]protocol.ServiceParameter
		output map[string]protocol.ServiceParameter
	}{
		{
			name:  "pipeline.increment",
			path:  "/increment",
			input: map[string]protocol.ServiceParameter{"seed": {Type: protocol.ParamTypeInt, Required: true}},
			output: map[string]protocol.ServiceParameter{
				"value": {Type: protocol.ParamTypeInt},
			},
		},
		{
			name:  "pipeline.double",
			path:  "/double",
			input: map[string]protocol.ServiceParameter{"value": {Type: protocol.ParamTypeInt, Required: true}},
			output: map[string]protocol.ServiceParameter{
				"result": {Type: protocol.ParamTypeInt},
			},
		},
		{
			name:  "pipeline.string",
			path:  "/string",
			input: map[string]protocol.ServiceParameter{"text": {Type: protocol.ParamTypeString, Required: true}},
			output: map[string]protocol.ServiceParameter{
				"accepted": {Type: protocol.ParamTypeString},
			},
		},
	}
	for _, service := range services {
		schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
			Name:       service.name,
			Type:       protocol.ServiceTypeGRPC,
			Parameters: service.input,
			Outputs:    service.output,
		})
		assertLiveBindSuccess(t, InvokeDomainAction("service", "add", map[string]string{
			"name":        service.name,
			"type":        string(protocol.ServiceTypeGRPC),
			"exec":        upstream.URL + service.path,
			"schema-file": schemaPath,
		}))
	}

	valid := protocol.PipelineSchema{
		ID:      "pipeline.valid",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "increment", Service: "pipeline.increment"},
			{ID: "double", Service: "pipeline.double"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "$initial", FromPort: "seed", ToStep: "increment", ToPort: "seed"},
			{FromStep: "increment", FromPort: "value", ToStep: "double", ToPort: "value"},
		},
	}
	assertLiveBindSuccess(t, AddPipelineRaw(valid.ID, marshalLiveJSON(t, valid)))

	var result protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, RunPipeline(valid.ID, `{"seed":4}`), &result)
	if result.Status != "completed" || result.Outputs["result"] != float64(10) {
		t.Fatalf("pipeline response = %#v, want completed result 10", result)
	}

	cyclic := protocol.PipelineSchema{
		ID: "pipeline.cyclic",
		Steps: []protocol.PipelineStep{
			{ID: "a", Service: "pipeline.increment"},
			{ID: "b", Service: "pipeline.double"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "a", FromPort: "value", ToStep: "b", ToPort: "value"},
			{FromStep: "b", FromPort: "result", ToStep: "a", ToPort: "seed"},
		},
	}
	assertLiveBindErrorContains(t, AddPipelineRaw(cyclic.ID, marshalLiveJSON(t, cyclic)), "cycle")

	typeInvalid := protocol.PipelineSchema{
		ID: "pipeline.type-invalid",
		Steps: []protocol.PipelineStep{
			{ID: "source", Service: "pipeline.increment"},
			{ID: "sink", Service: "pipeline.string"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "source", FromPort: "value", ToStep: "sink", ToPort: "text"},
		},
	}
	assertLiveBindErrorContains(t, AddPipelineRaw(typeInvalid.ID, marshalLiveJSON(t, typeInvalid)), "type mismatch")

	runtimeInvalid := RunPipeline(valid.ID, `{"seed":"not-an-int"}`)
	assertLiveBindErrorContains(t, runtimeInvalid, "invalid type")
	if !strings.Contains(ParseBindError(runtimeInvalid), "seed") {
		t.Fatalf("runtime validation error = %q, want seed context", ParseBindError(runtimeInvalid))
	}
}

func writeLiveServiceSchema(t *testing.T, schema protocol.ServiceSchema) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-schema.json")
	if err := os.WriteFile(path, []byte(marshalLiveJSON(t, schema)), 0o600); err != nil {
		t.Fatalf("write service schema: %v", err)
	}
	return path
}

func marshalLiveJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture JSON: %v", err)
	}
	return string(encoded)
}

func assertLiveBindSuccess(t *testing.T, result string) {
	t.Helper()
	if IsBindError(result) {
		t.Fatalf("bind action failed: %s", ParseBindError(result))
	}
}

func assertLiveBindErrorContains(t *testing.T, result string, expected string) {
	t.Helper()
	if !IsBindError(result) {
		t.Fatalf("bind action result = %s, want error containing %q", result, expected)
	}
	message := ParseBindError(result)
	if !strings.Contains(strings.ToLower(message), strings.ToLower(expected)) {
		t.Fatalf("bind error = %q, want %q", message, expected)
	}
}

func decodeLiveBindJSON(t *testing.T, result string, target any) {
	t.Helper()
	assertLiveBindSuccess(t, result)
	if err := json.Unmarshal([]byte(result), target); err != nil {
		t.Fatalf("decode bind response %q: %v", result, err)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func awaitLiveStreamChunk(t *testing.T, listener *liveStreamListener) map[string]any {
	t.Helper()
	select {
	case raw := <-listener.chunks:
		var chunk map[string]any
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("decode stream chunk %q: %v", raw, err)
		}
		return chunk
	case terminal := <-listener.terminal:
		t.Fatalf("stream terminated before expected chunk: %#v", terminal)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream chunk")
	}
	return nil
}

func awaitLiveStreamTerminal(t *testing.T, listener *liveStreamListener) liveStreamTerminal {
	t.Helper()
	select {
	case terminal := <-listener.terminal:
		return terminal
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream terminal event")
	}
	return liveStreamTerminal{}
}

func postLiveJoin(
	t *testing.T,
	client *http.Client,
	sponsorAddress string,
	request protocol.JoinRequest,
) int {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(
		sponsorAddress+protocol.PathClusterJoin,
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("post enrollment: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}
