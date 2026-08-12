package proxyma_bind

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
)

func TestStartNodeReturnsBindErrorWhenTCPPortIsUnavailable(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	port := strconv.Itoa(blocker.Addr().(*net.TCPAddr).Port)

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"blocked-bind-node",
		protocol.HTTPSAddr("127.0.0.1", port),
	); err != nil {
		t.Fatal(err)
	}

	result := StartNode(storagePath, false)
	if !IsBindError(result) {
		t.Fatalf("StartNode result = %q, want bind error", result)
	}
	if IsNodeRunning() {
		t.Fatal("IsNodeRunning reported true after listener bind failure")
	}
	if _, err := os.Stat(protocol.UnixSockPath(storagePath)); !os.IsNotExist(err) {
		t.Fatalf("failed startup left Unix socket behind: %v", err)
	}
}

func TestStartNodeReturnsWithUnixListenerReady(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"ready-bind-node",
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatal(err)
	}
	if result := StartNode(storagePath, false); result != "" {
		t.Fatalf("StartNode: %s", result)
	}
	if !IsNodeRunning() {
		t.Fatal("node is not ready after StartNode returned")
	}
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(cfg.Address, ":0") || cfg.Address != GetNodeAddress() {
		t.Fatalf("bound address was not published before readiness: config=%q runtime=%q", cfg.Address, GetNodeAddress())
	}
	socketInfo, err := os.Stat(protocol.UnixSockPath(storagePath))
	if err != nil {
		t.Fatal(err)
	}
	if got := socketInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("Unix socket mode = %#o, want 0600", got)
	}
	conn, err := DialUnix(storagePath)
	if err != nil {
		t.Fatalf("Unix listener not ready after StartNode returned: %v", err)
	}
	_ = conn.Close()
	StopNode()
	if _, err := os.Stat(protocol.UnixSockPath(storagePath)); !os.IsNotExist(err) {
		t.Fatalf("StopNode returned before Unix socket cleanup: %v", err)
	}
}

func TestStopBeforeDelayedBootstrapIsSafe(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	entered := make(chan struct{})
	exited := make(chan struct{})
	srvMutex.Lock()
	originalWait := startupBootstrapWait
	startupBootstrapWait = func(ctx context.Context) bool {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return false
	}
	srvMutex.Unlock()
	t.Cleanup(func() {
		srvMutex.Lock()
		startupBootstrapWait = originalWait
		srvMutex.Unlock()
	})

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"delayed-bootstrap-node",
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BootstrapNode = "https://127.0.0.1:1"
	if err := protocol.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	if result := StartNode(storagePath, false); result != "" {
		t.Fatalf("StartNode: %s", result)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed bootstrap goroutine did not start")
	}
	StopNode()

	select {
	case <-exited:
	default:
		t.Fatal("StopNode returned before joining delayed bootstrap")
	}
	if IsNodeRunning() {
		t.Fatal("node became running again after delayed bootstrap")
	}
}

func TestStopNodeRetainsStoppingStateUntilShutdownFinalizes(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	srvMutex.Lock()
	originalTimeout := nodeShutdownTimeout
	nodeShutdownTimeout = 20 * time.Millisecond
	srvMutex.Unlock()
	t.Cleanup(func() {
		srvMutex.Lock()
		nodeShutdownTimeout = originalTimeout
		srvMutex.Unlock()
	})

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"stopping-node",
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatal(err)
	}
	if result := StartNode(storagePath, false); result != "" {
		t.Fatalf("StartNode: %s", result)
	}
	startedServer := getSrv()
	_, release, err := startedServer.AcquireWorkLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	stopResult := StopNodeWithError()
	if !IsBindError(stopResult) {
		t.Fatalf("StopNodeWithError result = %q, want timeout bind error", stopResult)
	}
	if !IsNodeStopping() || GetNodeID() != "stopping-node" {
		t.Fatalf("stopping globals cleared early: stopping=%v id=%q", IsNodeStopping(), GetNodeID())
	}

	release()
	select {
	case <-startedServer.ShutdownDone():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finalize after releasing owned work")
	}
}

func TestUnexpectedListenerExitClosesBackgroundWorkAdmissionBeforeWait(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	waitEntered := make(chan struct{})
	cancelObserved := make(chan struct{})
	releaseWait := make(chan struct{})
	srvMutex.Lock()
	originalWait := startupBootstrapWait
	startupBootstrapWait = func(ctx context.Context) bool {
		close(waitEntered)
		<-ctx.Done()
		close(cancelObserved)
		<-releaseWait
		return false
	}
	srvMutex.Unlock()
	t.Cleanup(func() {
		srvMutex.Lock()
		startupBootstrapWait = originalWait
		srvMutex.Unlock()
	})

	storagePath := t.TempDir()
	if err := p2p.SetupNewNode(
		storagePath,
		"unexpected-exit-node",
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BootstrapNode = "https://127.0.0.1:1"
	if err := protocol.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if result := StartNode(storagePath, false); result != "" {
		t.Fatalf("StartNode: %s", result)
	}
	select {
	case <-waitEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed work did not start")
	}

	startedServer := getSrv()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- startedServer.Shutdown(context.Background())
	}()
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected listener exit did not start bind finalization")
	}

	accepted := startNodeBackgroundWork(startedServer, func(context.Context) {})
	close(releaseWait)
	if accepted {
		t.Fatal("background work was admitted after unexpected exit began waiting")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("external server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external server shutdown did not finish")
	}
	if result := StopNodeWithError(); result != "" {
		t.Fatalf("join bind finalizer: %s", result)
	}
}

func TestJoinClusterRollsBackCertificatesAndConfigWhenInstallFails(t *testing.T) {
	storagePath, oldState := prepareExistingJoinState(t)
	restoreJoinSeams := installSuccessfulJoinRemote(t)
	defer restoreJoinSeams()

	originalRename := joinRename
	joinRename = func(oldPath, newPath string) error {
		if filepath.Base(oldPath) == "config.json" &&
			filepath.Base(filepath.Dir(oldPath)) == "state" &&
			newPath == filepath.Join(storagePath, "config.json") {
			return errors.New("injected config install failure")
		}
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() { joinRename = originalRename })

	result := JoinCluster(storagePath, "invite", "new-node", protocol.DefaultTCPPort)
	if !IsBindError(result) || !strings.Contains(ParseBindError(result), "config install failure") {
		t.Fatalf("JoinCluster result = %s, want injected install error", result)
	}
	assertJoinStateRestored(t, oldState)
	assertNoJoinTransactionArtifacts(t, storagePath)
}

func TestJoinClusterRollsBackCertificatesAndConfigWhenStartFails(t *testing.T) {
	storagePath, oldState := prepareExistingJoinState(t)
	restoreJoinSeams := installSuccessfulJoinRemote(t)
	defer restoreJoinSeams()

	originalStart := startJoinedNode
	startJoinedNode = func(string, bool) string {
		return BindErrorJSON(errors.New("injected joined node start failure"))
	}
	t.Cleanup(func() { startJoinedNode = originalStart })

	result := JoinCluster(storagePath, "invite", "new-node", protocol.DefaultTCPPort)
	if !IsBindError(result) || !strings.Contains(ParseBindError(result), "joined node start failure") {
		t.Fatalf("JoinCluster result = %s, want injected start error", result)
	}
	assertJoinStateRestored(t, oldState)
	assertNoJoinTransactionArtifacts(t, storagePath)
}

func TestJoinClusterRestartsPreviousReadyNodeAfterRollback(t *testing.T) {
	storagePath, oldState := prepareExistingJoinState(t)
	restoreJoinSeams := installSuccessfulJoinRemote(t)
	defer restoreJoinSeams()

	if result := StartNode(storagePath, false); result != "" {
		t.Fatalf("start previous node: %s", result)
	}
	if !IsNodeRunning() {
		t.Fatal("previous node was not ready before join")
	}
	configPath := filepath.Join(storagePath, "config.json")
	refreshedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	oldState[configPath] = refreshedConfig

	originalStart := startJoinedNode
	var startCalls atomic.Int32
	startJoinedNode = func(path string, debug bool) string {
		if startCalls.Add(1) == 1 {
			return BindErrorJSON(errors.New("injected joined node start failure"))
		}
		return startNode(path, debug)
	}
	t.Cleanup(func() { startJoinedNode = originalStart })

	result := JoinCluster(storagePath, "invite", "new-node", protocol.DefaultTCPPort)
	if !IsBindError(result) {
		t.Fatalf("JoinCluster result = %s, want joined node start failure", result)
	}
	if !IsNodeRunning() || GetNodeID() != "old-node" {
		t.Fatalf("previous node was not restored: running=%v id=%q", IsNodeRunning(), GetNodeID())
	}
	assertJoinStateRestored(t, oldState)
	assertNoJoinTransactionArtifacts(t, storagePath)
}

func TestJoinClusterWildcardBootstrapUsesSponsorIdentity(t *testing.T) {
	storagePath, _ := prepareExistingJoinState(t)
	caPath, _ := p2p.CACertPaths(filepath.Join(storagePath, "certs"))
	token, _, err := p2p.GenerateSmartToken(
		"https://0.0.0.0:8443",
		caPath,
		"sponsor-node",
		"https://relay.example:8443",
	)
	if err != nil {
		t.Fatal(err)
	}

	originalRemote := joinClusterRemote
	joinClusterRemote = func(
		context.Context,
		string,
		string,
		string,
		func(string, error),
	) (string, string, []byte, string, error) {
		return "new-ca", "new-cert", []byte("new-key"), "https://0.0.0.0:8443", nil
	}
	t.Cleanup(func() { joinClusterRemote = originalRemote })
	originalStart := startJoinedNode
	startJoinedNode = func(string, bool) string { return "" }
	t.Cleanup(func() { startJoinedNode = originalStart })

	if result := JoinCluster(storagePath, token, "joining-node", protocol.DefaultTCPPort); result != "" {
		t.Fatalf("JoinCluster: %s", result)
	}
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BootstrapNode != "https://sponsor-node:8443" {
		t.Fatalf("bootstrap = %q, want sponsor identity", cfg.BootstrapNode)
	}
}

func TestConcurrentJoinClusterCallsAreSerialized(t *testing.T) {
	firstStorage, _ := prepareExistingJoinState(t)
	secondStorage, _ := prepareExistingJoinState(t)

	originalRemote := joinClusterRemote
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var calls atomic.Int32
	joinClusterRemote = func(
		context.Context,
		string,
		string,
		string,
		func(string, error),
	) (string, string, []byte, string, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return "new-ca", "new-cert", []byte("new-key"), "https://sponsor:8080", nil
	}
	t.Cleanup(func() { joinClusterRemote = originalRemote })
	originalStart := startJoinedNode
	startJoinedNode = func(string, bool) string { return "" }
	t.Cleanup(func() { startJoinedNode = originalStart })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = JoinCluster(firstStorage, "invite", "first", protocol.DefaultTCPPort)
	}()
	<-firstEntered
	go func() {
		defer wg.Done()
		_ = JoinCluster(secondStorage, "invite", "second", protocol.DefaultTCPPort)
	}()
	select {
	case <-secondEntered:
		t.Fatal("second join entered network phase before first completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	wg.Wait()
}

func TestJoinInstallationRecoversInterruptedSwap(t *testing.T) {
	storagePath, oldState := prepareExistingJoinState(t)
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = "new-node"
	installation, err := stageJoinInstallation(
		storagePath,
		"new-node",
		cfg,
		[]byte("new-ca"),
		[]byte("new-cert"),
		[]byte("new-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := installation.install(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(storagePath, joinJournalFileName)); err != nil {
		t.Fatalf("durable join journal missing after swap: %v", err)
	}

	if err := recoverJoinInstallation(storagePath); err != nil {
		t.Fatal(err)
	}
	assertJoinStateRestored(t, oldState)
	assertNoJoinTransactionArtifacts(t, storagePath)
}

func prepareExistingJoinState(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	StopNode()
	t.Cleanup(StopNode)

	storagePath := t.TempDir()
	nodeID := "old-node"
	if err := p2p.SetupNewNode(
		storagePath,
		nodeID,
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatal(err)
	}
	certsDir := filepath.Join(storagePath, "certs")
	caPath, caKeyPath := p2p.CACertPaths(certsDir)
	nodeCertPath, nodeKeyPath := p2p.NodeCertPaths(certsDir, nodeID)
	paths := []string{
		filepath.Join(storagePath, "config.json"),
		caPath,
		caKeyPath,
		nodeCertPath,
		nodeKeyPath,
	}
	state := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		state[path] = content
	}
	return storagePath, state
}

func installSuccessfulJoinRemote(t *testing.T) func() {
	t.Helper()
	originalRemote := joinClusterRemote
	joinClusterRemote = func(
		context.Context,
		string,
		string,
		string,
		func(string, error),
	) (string, string, []byte, string, error) {
		return "new-ca", "new-cert", []byte("new-key"), "https://sponsor:8080", nil
	}
	t.Cleanup(func() { joinClusterRemote = originalRemote })
	return func() { joinClusterRemote = originalRemote }
}

func assertJoinStateRestored(t *testing.T, oldState map[string][]byte) {
	t.Helper()
	for path, want := range oldState {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("state at %s was not restored", path)
		}
	}
}

func assertNoJoinTransactionArtifacts(t *testing.T, storagePath string) {
	t.Helper()
	entries, err := os.ReadDir(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".join-") ||
			entry.Name() == "certs.staging" ||
			entry.Name() == "certs.bak" {
			t.Fatalf("join transaction artifact remains: %s", entry.Name())
		}
	}
}
