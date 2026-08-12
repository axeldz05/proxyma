package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

const (
	liveCLIContractDaemonEnv        = "PROXYMA_CLI_CONTRACT_DAEMON"
	liveCLIContractDaemonStorageEnv = "PROXYMA_CLI_CONTRACT_STORAGE"
	liveCLIContractReadyMarker      = "PROXYMA_CLI_CONTRACT_READY"
)

func TestPeersListDaemonCmd(t *testing.T) {
	resetRootCommandState(t)
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "peers", req.Action)
		return []map[string]any{
			{"id": "peer-a", "online": true},
		}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"peers", "list", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestTelemetryStatsDaemonCmd(t *testing.T) {
	resetRootCommandState(t)
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "bandwidth", req.Action)
		return []map[string]any{
			{"metric": "Download Speed", "value": "20 B/s"},
			{"metric": "Upload Speed", "value": "10 B/s"},
			{"metric": "Total Received", "value": "200 B"},
			{"metric": "Total Sent", "value": "100 B"},
		}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"telemetry", "stats", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestPipelineListDaemonCmd(t *testing.T) {
	resetRootCommandState(t)
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "pipeline_list", req.Action)
		return []protocol.PipelineSchema{{ID: "p1"}}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"service", "list_pipelines", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestPeersListBindErrorPropagates(t *testing.T) {
	resetRootCommandState(t)
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		return nil, errString("daemon down")
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"peers", "list", "--storage", tempDir})
	err := rootCmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "daemon down")
}

type errString string

func (e errString) Error() string { return string(e) }

func TestCLIContractLiveDaemon(t *testing.T) {
	if os.Getenv(liveCLIContractDaemonEnv) == "1" {
		runLiveCLIContractDaemonFixture(t)
		return
	}

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"cli-contract-node",
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatalf("set up node: %v", err)
	}

	binaryPath := filepath.Join(t.TempDir(), "proxyma")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	daemon := startLiveCLIContractDaemon(t, storagePath)

	help := mustRunLiveCLIContractCommand(t, binaryPath, "storage", "upload", "--help")
	assertLiveCLIContractOutput(t, help, "Usage:", "--path", "--name")

	inputDir := t.TempDir()
	defaultPath := filepath.Join(inputDir, "default-contract.txt")
	if err := os.WriteFile(defaultPath, []byte("default-name transport"), 0o600); err != nil {
		t.Fatalf("write default-name input: %v", err)
	}
	defaultUpload := mustRunLiveCLIContractCommand(
		t,
		binaryPath,
		"--storage", storagePath,
		"storage", "upload",
		"--path", defaultPath,
	)
	assertLiveCLIContractOutput(
		t,
		defaultUpload,
		"File 'default-contract.txt' uploaded successfully to VFS.",
	)

	explicitPath := filepath.Join(inputDir, "source-contract.txt")
	if err := os.WriteFile(explicitPath, []byte("explicit-name transport"), 0o600); err != nil {
		t.Fatalf("write explicit-name input: %v", err)
	}
	explicitUpload := mustRunLiveCLIContractCommand(
		t,
		binaryPath,
		"storage", "upload",
		"--path", explicitPath,
		"--name", "renamed-contract.txt",
		"--storage", storagePath,
	)
	assertLiveCLIContractOutput(
		t,
		explicitUpload,
		"File 'renamed-contract.txt' uploaded successfully to VFS.",
	)

	list := mustRunLiveCLIContractCommand(
		t,
		binaryPath,
		"storage", "list",
		"--storage", storagePath,
	)
	assertLiveCLIContractOutput(
		t,
		list,
		"NAME",
		"default-contract.txt",
		"renamed-contract.txt",
	)

	stats := mustRunLiveCLIContractCommand(
		t,
		binaryPath,
		"telemetry", "stats",
		"--storage", storagePath,
	)
	assertLiveCLIContractOutput(t, stats, "METRIC", "VALUE", "Download Speed", "Total Sent")

	if err := daemon.stop(); err != nil {
		t.Fatalf("stop daemon: %v\n%s", err, daemon.output.String())
	}
}

func runLiveCLIContractDaemonFixture(t *testing.T) {
	result := proxyma_bind.StartNode(os.Getenv(liveCLIContractDaemonStorageEnv), false)
	if result != "" {
		t.Fatalf("StartNode: %s", proxyma_bind.ParseBindError(result))
	}
	fmt.Println(liveCLIContractReadyMarker)

	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	stopResult := proxyma_bind.StopNodeWithError()
	if stopResult != "" {
		t.Fatalf("StopNodeWithError: %s", proxyma_bind.ParseBindError(stopResult))
	}
}

func mustRunLiveCLIContractCommand(t *testing.T, binaryPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		t.Fatalf("CLI %q exit code = %d, want 0: %v\n%s", strings.Join(args, " "), exitCode, err, output)
	}
	return string(output)
}

func assertLiveCLIContractOutput(t *testing.T, output string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(output, fragment) {
			t.Fatalf("CLI output = %q, want fragment %q", output, fragment)
		}
	}
}

type liveCLIContractDaemon struct {
	stdin    io.WriteCloser
	process  *os.Process
	wait     <-chan error
	output   *liveCLIContractOutput
	stopOnce sync.Once
	stopErr  error
}

type liveCLIContractOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *liveCLIContractOutput) add(line string) {
	o.mu.Lock()
	o.lines = append(o.lines, line)
	o.mu.Unlock()
}

func (o *liveCLIContractOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.lines, "\n")
}

func startLiveCLIContractDaemon(t *testing.T, storagePath string) *liveCLIContractDaemon {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestCLIContractLiveDaemon$")
	cmd.Env = append(
		os.Environ(),
		liveCLIContractDaemonEnv+"=1",
		liveCLIContractDaemonStorageEnv+"="+storagePath,
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

	ready := make(chan struct{}, 1)
	output := &liveCLIContractOutput{}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			if line == liveCLIContractReadyMarker {
				ready <- struct{}{}
			}
		}
		if err := scanner.Err(); err != nil {
			output.add("scan daemon output: " + err.Error())
		}
	}()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon process: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	select {
	case <-ready:
	case err := <-wait:
		t.Fatalf("daemon exited before readiness: %v\n%s", err, output.String())
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-wait
		t.Fatalf("daemon readiness timed out\n%s", output.String())
	}

	daemon := &liveCLIContractDaemon{
		stdin:   stdin,
		process: cmd.Process,
		wait:    wait,
		output:  output,
	}
	t.Cleanup(func() {
		if err := daemon.stop(); err != nil {
			t.Errorf("clean up daemon: %v\n%s", err, output.String())
		}
	})
	return daemon
}

func (d *liveCLIContractDaemon) stop() error {
	d.stopOnce.Do(func() {
		if _, err := io.WriteString(d.stdin, "stop\n"); err != nil {
			d.stopErr = fmt.Errorf("request daemon stop: %w", err)
			_ = d.stdin.Close()
			_ = d.process.Kill()
			<-d.wait
			return
		}
		_ = d.stdin.Close()

		select {
		case err := <-d.wait:
			if err != nil {
				d.stopErr = fmt.Errorf("daemon process: %w", err)
			}
		case <-time.After(10 * time.Second):
			d.stopErr = fmt.Errorf("daemon shutdown timed out")
			_ = d.process.Kill()
			<-d.wait
		}
	})
	return d.stopErr
}
