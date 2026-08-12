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
	"net/url"
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
	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		nodeID,
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatalf("set up live node: %v", err)
	}
	return startLiveBindDaemonAt(t, storagePath)
}

func startLiveBindDaemonAt(t *testing.T, storagePath string) *liveBindDaemon {
	t.Helper()
	StopNode()

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

func TestLiveDaemonTelemetryAndAndroidMetadataContracts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}))
	t.Cleanup(upstream.Close)

	startLiveBindDaemon(t, "live-metadata")
	schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
		Name:        "live.metadata",
		Type:        protocol.ServiceTypeServerStream,
		Description: "Android metadata contract",
		Parameters: map[string]protocol.ServiceParameter{
			"query": {
				Type:     protocol.ParamTypeString,
				Required: true,
			},
			"mode": {
				Type:    protocol.ParamTypeString,
				Options: []string{"fast", "quality"},
			},
			"document": {
				Type:   protocol.ParamTypeFile,
				UIHint: protocol.UIHintFilePicker,
			},
			"photo": {
				Type:   protocol.ParamTypeFile,
				UIHint: protocol.UIHintImagePicker,
			},
		},
	})
	assertLiveBindSuccess(t, AddService(
		"live.metadata",
		string(protocol.ServiceTypeServerStream),
		upstream.URL,
		"Android metadata contract",
		"",
		"",
		schemaPath,
	))

	var detail ServiceDetail
	decodeLiveBindJSON(t, GetServiceDetails("live.metadata"), &detail)
	if detail.Name != "live.metadata" ||
		detail.Description != "Android metadata contract" ||
		!detail.IsStreaming {
		t.Fatalf("service detail = %#v, want named streaming Android metadata", detail)
	}
	parameters := make(map[string]ParameterDetail, len(detail.Parameters))
	for _, parameter := range detail.Parameters {
		parameters[parameter.Name] = parameter
	}
	if query := parameters["query"]; !query.Required || query.Type != protocol.ParamTypeString {
		t.Fatalf("required query parameter = %#v", query)
	}
	if mode := parameters["mode"]; mode.Required ||
		mode.Type != protocol.ParamTypeString ||
		len(mode.Options) != 2 ||
		mode.Options[0] != "fast" ||
		mode.Options[1] != "quality" {
		t.Fatalf("optional mode parameter = %#v", mode)
	}
	if document := parameters["document"]; document.Required ||
		document.Type != protocol.ParamTypeFile ||
		document.UIHint != protocol.UIHintFilePicker {
		t.Fatalf("document parameter = %#v, want optional file picker", document)
	}
	if photo := parameters["photo"]; photo.Required ||
		photo.Type != protocol.ParamTypeFile ||
		photo.UIHint != protocol.UIHintImagePicker {
		t.Fatalf("photo parameter = %#v, want optional image picker", photo)
	}
	if !containsString(detail.RequiredPermissions, "Camera (to take photo for upload)") ||
		!containsString(detail.RequiredPermissions, "Gallery / Storage (to select photo)") {
		t.Fatalf("required permissions = %#v, want camera and gallery metadata", detail.RequiredPermissions)
	}

	var stats []struct {
		Metric string `json:"metric"`
		Value  string `json:"value"`
	}
	decodeLiveBindJSON(t, GetBandwidthStatsJson(), &stats)
	statValues := make(map[string]string, len(stats))
	for _, stat := range stats {
		statValues[stat.Metric] = stat.Value
	}
	for _, metric := range []string{"Download Speed", "Upload Speed", "Total Received", "Total Sent"} {
		if strings.TrimSpace(statValues[metric]) == "" {
			t.Fatalf("bandwidth stats = %#v, want non-empty %q value", stats, metric)
		}
	}

	var logs []protocol.LogRecord
	decodeLiveBindJSON(t, GetLogsJson(), &logs)
	foundActionLog := false
	for _, record := range logs {
		if record.Timestamp != "" &&
			record.Level != "" &&
			strings.Contains(record.Message, "Local service registered") &&
			strings.Contains(record.Message, "live.metadata") {
			foundActionLog = true
			break
		}
	}
	if !foundActionLog {
		t.Fatalf("logs = %#v, want public service-add action record", logs)
	}
}

func TestLiveDaemonStorageLifecycleContracts(t *testing.T) {
	startLiveBindDaemon(t, "live-storage-lifecycle")

	const (
		name    = "lifecycle.txt"
		content = "live storage lifecycle"
	)
	inputPath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(inputPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	assertLiveBindSuccess(t, UploadFile(name, inputPath))
	uploaded := liveVFSFile(t, name)
	if !uploaded.HasLocal || !uploaded.Subscribed || uploaded.Deleted ||
		uploaded.Size != int64(len(content)) || uploaded.Hash == "" {
		t.Fatalf("uploaded VFS status = %#v, want subscribed local live file", uploaded)
	}

	assertLiveBindSuccess(t, SetSubscription(name, false))
	unsubscribed := liveVFSFile(t, name)
	if unsubscribed.Subscribed {
		t.Fatalf("unsubscribed VFS status = %#v, want subscribed=false", unsubscribed)
	}

	assertLiveBindSuccess(t, SetSubscription(name, true))
	subscribed := liveVFSFile(t, name)
	if !subscribed.Subscribed {
		t.Fatalf("subscribed VFS status = %#v, want subscribed=true", subscribed)
	}

	assertLiveBindSuccess(t, SyncVFS())
	localPath := ResolveLocalBlob(name)
	assertLiveBindSuccess(t, localPath)
	localContent, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read resolved local blob %q: %v", localPath, err)
	}
	if string(localContent) != content {
		t.Fatalf("resolved local blob = %q, want %q", localContent, content)
	}

	assertLiveBindSuccess(t, DeleteLocalCache(name))
	purged := liveVFSFile(t, name)
	if purged.HasLocal || purged.Subscribed || purged.Deleted {
		t.Fatalf("purged VFS status = %#v, want remote-only live metadata", purged)
	}
	assertLiveBindErrorContains(t, FetchFileOnDemand(name), "no peer holds physical replica")

	assertLiveBindSuccess(t, DeleteFile(name))
	tombstone := liveVFSFile(t, name)
	if !tombstone.Deleted || tombstone.HasLocal || tombstone.Subscribed {
		t.Fatalf("deleted VFS status = %#v, want nonlocal tombstone", tombstone)
	}
	if tombstone.Version <= uploaded.Version {
		t.Fatalf("tombstone version = %d, want greater than upload version %d", tombstone.Version, uploaded.Version)
	}
}

func TestLiveDaemonTaskStatusPollingContracts(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	releaseUpstream := func() {
		releaseOnce.Do(func() { close(release) })
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": payload["value"]})
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(upstream.Close)

	startLiveBindDaemon(t, "live-task-status")
	t.Cleanup(releaseUpstream)
	schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
		Name: "live.gated",
		Type: protocol.ServiceTypeGRPC,
		Parameters: map[string]protocol.ServiceParameter{
			"value": {Type: protocol.ParamTypeInt, Required: true},
		},
		Outputs: map[string]protocol.ServiceParameter{
			"result": {Type: protocol.ParamTypeInt},
		},
	})
	assertLiveBindSuccess(t, AddService(
		"live.gated",
		string(protocol.ServiceTypeGRPC),
		upstream.URL,
		"",
		"",
		"",
		schemaPath,
	))

	runResult := make(chan string, 1)
	go func() {
		runResult <- RunService("live.gated", `{"value":9}`)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("gated upstream did not receive the service request")
	}

	var statuses []protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, GetTaskStatus(""), &statuses)
	pending, ok := liveTaskForService(statuses, "live.gated")
	if !ok || pending.TaskID == "" || pending.Status != "pending" {
		t.Fatalf("task statuses = %#v, want pending live.gated task with ID", statuses)
	}

	var polledPending protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, GetTaskStatus(pending.TaskID), &polledPending)
	if polledPending.TaskID != pending.TaskID || polledPending.Status != "pending" {
		t.Fatalf("pending task poll = %#v, want task %q pending", polledPending, pending.TaskID)
	}

	releaseUpstream()
	runJSON := awaitLiveBindResult(t, runResult, "RunService completion")
	var completed protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, runJSON, &completed)
	if completed.TaskID != pending.TaskID || completed.Status != "completed" ||
		completed.Outputs["result"] != float64(9) {
		t.Fatalf("completed RunService response = %#v, want task %q result 9", completed, pending.TaskID)
	}

	var polledCompleted protocol.ServiceTaskResponse
	decodeLiveBindJSON(t, GetTaskStatus(pending.TaskID), &polledCompleted)
	if polledCompleted.Status != "completed" || polledCompleted.Outputs["result"] != float64(9) {
		t.Fatalf("completed task poll = %#v, want completed result 9", polledCompleted)
	}
	assertLiveBindErrorContains(t, GetTaskStatus("task-does-not-exist"), "task not found")
}

func TestLiveDaemonStatePersistsAcrossRestart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": payload["value"]})
	}))
	t.Cleanup(upstream.Close)

	first := startLiveBindDaemon(t, "live-persistence")
	schemaPath := writeLiveServiceSchema(t, protocol.ServiceSchema{
		Name:        "persistence.echo",
		Type:        protocol.ServiceTypeGRPC,
		Description: "restart persistence contract",
		Parameters: map[string]protocol.ServiceParameter{
			"value": {Type: protocol.ParamTypeInt, Required: true},
		},
		Outputs: map[string]protocol.ServiceParameter{
			"result": {Type: protocol.ParamTypeInt},
		},
	})
	assertLiveBindSuccess(t, AddService(
		"persistence.echo",
		string(protocol.ServiceTypeGRPC),
		upstream.URL,
		"restart persistence contract",
		"",
		"",
		schemaPath,
	))

	pipeline := protocol.PipelineSchema{
		ID:      "persistence.pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "echo", Service: "persistence.echo"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "$initial", FromPort: "value", ToStep: "echo", ToPort: "value"},
		},
	}
	assertLiveBindSuccess(t, AddPipelineRaw(pipeline.ID, marshalLiveJSON(t, pipeline)))

	const (
		vfsName    = "persistent.txt"
		vfsContent = "persistent VFS content"
	)
	inputPath := filepath.Join(t.TempDir(), vfsName)
	if err := os.WriteFile(inputPath, []byte(vfsContent), 0o600); err != nil {
		t.Fatal(err)
	}
	assertLiveBindSuccess(t, UploadFile(vfsName, inputPath))
	beforeRestart := liveVFSFile(t, vfsName)

	if err := first.stop(); err != nil {
		t.Fatalf("stop first live daemon: %v\n%s", err, first.output.String())
	}
	startLiveBindDaemonAt(t, first.storage)

	var discovered []string
	decodeLiveBindJSON(t, DiscoverServices(), &discovered)
	if !containsString(discovered, "persistence.echo") {
		t.Fatalf("services after restart = %#v, want persistence.echo", discovered)
	}
	var persistedService protocol.ServiceSchema
	decodeLiveBindJSON(t, GetServiceSchema("persistence.echo"), &persistedService)
	if persistedService.Name != "persistence.echo" ||
		persistedService.Description != "restart persistence contract" ||
		persistedService.Parameters["value"].Type != protocol.ParamTypeInt {
		t.Fatalf("service schema after restart = %#v", persistedService)
	}

	var pipelines []protocol.PipelineSchema
	decodeLiveBindJSON(t, ListPipelines(), &pipelines)
	persistedPipeline, ok := livePipelineByID(pipelines, pipeline.ID)
	if !ok || protocol.PipelineSchemaHash(persistedPipeline) != protocol.PipelineSchemaHash(pipeline) {
		t.Fatalf("pipeline list after restart = %#v, want %#v", pipelines, pipeline)
	}
	var fetchedPipeline protocol.PipelineSchema
	decodeLiveBindJSON(t, GetPipelineSchemaJson(pipeline.ID), &fetchedPipeline)
	if protocol.PipelineSchemaHash(fetchedPipeline) != protocol.PipelineSchemaHash(pipeline) {
		t.Fatalf("pipeline get after restart = %#v, want %#v", fetchedPipeline, pipeline)
	}

	afterRestart := liveVFSFile(t, vfsName)
	if afterRestart.Hash != beforeRestart.Hash || !afterRestart.HasLocal ||
		!afterRestart.Subscribed || afterRestart.Deleted {
		t.Fatalf("VFS status after restart = %#v, before restart %#v", afterRestart, beforeRestart)
	}
	localPath := ResolveLocalBlob(vfsName)
	assertLiveBindSuccess(t, localPath)
	gotContent, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read VFS blob after restart %q: %v", localPath, err)
	}
	if string(gotContent) != vfsContent {
		t.Fatalf("VFS content after restart = %q, want %q", gotContent, vfsContent)
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

	// JoinCluster intentionally installs and starts the joined singleton in this
	// process. Exercise its public bind boundary directly instead of adding a
	// subprocess-only production seam.
	joinerConfig, err := protocol.LoadConfig(joinerStorage)
	if err != nil {
		t.Fatalf("load joined node config: %v", err)
	}
	joinerURL, err := url.Parse(joinerConfig.Address)
	if err != nil {
		t.Fatalf("parse joined node address: %v", err)
	}
	joinerCert, joinerKey := p2p.NodeCertPaths(
		filepath.Dir(joinerConfig.CAPath),
		joinerConfig.ID,
	)
	_, joinerClientTLS, err := p2p.LoadNodeTLS(joinerConfig.CAPath, joinerCert, joinerKey)
	if err != nil {
		t.Fatalf("load joined node client TLS: %v", err)
	}
	enrollmentBody, err := json.Marshal(protocol.AddPeerRequest{
		ID: sponsorConfig.ID,
		Address: protocol.AddressRecord{
			Addresses: []string{sponsorConfig.Address},
		},
	})
	if err != nil {
		t.Fatalf("marshal peer enrollment: %v", err)
	}
	enrollmentClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: joinerClientTLS},
		Timeout:   3 * time.Second,
	}
	enrollmentResponse, err := enrollmentClient.Post(
		protocol.HTTPSAddr("localhost", joinerURL.Port())+protocol.PathPeersAdd,
		"application/json",
		bytes.NewReader(enrollmentBody),
	)
	if err != nil {
		t.Fatalf("enroll sponsor through public peer API: %v", err)
	}
	_, _ = io.Copy(io.Discard, enrollmentResponse.Body)
	_ = enrollmentResponse.Body.Close()
	if enrollmentResponse.StatusCode != http.StatusOK {
		t.Fatalf("peer enrollment status = %d, want %d", enrollmentResponse.StatusCode, http.StatusOK)
	}

	var peers []protocol.PeerStatus
	decodeLiveBindJSON(t, GetPeersJson(), &peers)
	var enrolledSponsor protocol.PeerStatus
	for _, peer := range peers {
		if peer.ID == "live-sponsor" {
			enrolledSponsor = peer
			break
		}
	}
	if enrolledSponsor.Address == "" ||
		!enrolledSponsor.Online ||
		enrolledSponsor.Error != "" {
		t.Fatalf("joined peers = %#v, want online live-sponsor DTO", peers)
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

	var pipelines []protocol.PipelineSchema
	decodeLiveBindJSON(t, ListPipelines(), &pipelines)
	listed, ok := livePipelineByID(pipelines, valid.ID)
	if !ok || protocol.PipelineSchemaHash(listed) != protocol.PipelineSchemaHash(valid) {
		t.Fatalf("pipeline list = %#v, want source schema %#v", pipelines, valid)
	}

	var fetched protocol.PipelineSchema
	decodeLiveBindJSON(t, GetPipelineSchemaJson(valid.ID), &fetched)
	if protocol.PipelineSchemaHash(fetched) != protocol.PipelineSchemaHash(valid) {
		t.Fatalf("pipeline get = %#v, want %#v", fetched, valid)
	}

	var cloned protocol.PipelineSchema
	decodeLiveBindJSON(
		t,
		ClonePipelineSchemaJson(valid.ID, "pipeline.cloned", "$local"),
		&cloned,
	)
	if cloned.ID != "pipeline.cloned" ||
		len(cloned.Steps) != len(valid.Steps) ||
		len(cloned.Connections) != len(valid.Connections) {
		t.Fatalf("pipeline clone = %#v, want renamed source topology", cloned)
	}
	for _, step := range cloned.Steps {
		if step.TargetNodeID != "live-pipeline" {
			t.Fatalf("pipeline clone step = %#v, want local target live-pipeline", step)
		}
	}
	var afterClone []protocol.PipelineSchema
	decodeLiveBindJSON(t, ListPipelines(), &afterClone)
	if _, persisted := livePipelineByID(afterClone, cloned.ID); persisted {
		t.Fatalf("pipeline clone unexpectedly persisted: %#v", afterClone)
	}

	assertLiveBindSuccess(t, RemovePipeline(valid.ID))
	var afterRemove []protocol.PipelineSchema
	decodeLiveBindJSON(t, ListPipelines(), &afterRemove)
	if _, exists := livePipelineByID(afterRemove, valid.ID); exists {
		t.Fatalf("removed pipeline still listed: %#v", afterRemove)
	}
	assertLiveBindErrorContains(t, GetPipelineSchemaJson(valid.ID), "not found")
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

func liveVFSFile(t *testing.T, name string) protocol.VFSFileStatus {
	t.Helper()
	var files []protocol.VFSFileStatus
	decodeLiveBindJSON(t, GetVFSFilesJson(), &files)
	for _, file := range files {
		if file.Name == name {
			return file
		}
	}
	t.Fatalf("VFS files = %#v, want %q", files, name)
	return protocol.VFSFileStatus{}
}

func liveTaskForService(
	statuses []protocol.ServiceTaskResponse,
	service string,
) (protocol.ServiceTaskResponse, bool) {
	for _, status := range statuses {
		if status.Service == service {
			return status, true
		}
	}
	return protocol.ServiceTaskResponse{}, false
}

func livePipelineByID(
	pipelines []protocol.PipelineSchema,
	id string,
) (protocol.PipelineSchema, bool) {
	for _, pipeline := range pipelines {
		if pipeline.ID == id {
			return pipeline, true
		}
	}
	return protocol.PipelineSchema{}, false
}

func awaitLiveBindResult(t *testing.T, results <-chan string, operation string) string {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
	return ""
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
