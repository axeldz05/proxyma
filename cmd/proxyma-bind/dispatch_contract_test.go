package proxyma_bind

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/shared/uischema"
)

func TestBindErrorJSONAlwaysProducesValidJSON(t *testing.T) {
	message := "quote=\" newline=\n invalid=\xff"
	encoded := BindErrorJSON(errors.New(message))
	if !json.Valid([]byte(encoded)) {
		t.Fatalf("BindErrorJSON returned invalid JSON: %q", encoded)
	}
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envelope.Error, "quote=\"") || !strings.Contains(envelope.Error, "newline=\n") {
		t.Fatalf("error envelope lost valid content: %q", envelope.Error)
	}
}

func TestDaemonUnavailableClassificationRejectsAmbiguousDialErrors(t *testing.T) {
	if !isUnavailableDialError(syscall.ENOENT) {
		t.Fatal("missing Unix socket was not classified as unavailable")
	}
	if !isUnavailableDialError(syscall.ECONNREFUSED) {
		t.Fatal("refused Unix socket was not classified as unavailable")
	}
	if isUnavailableDialError(os.ErrPermission) {
		t.Fatal("Unix socket permission failure was classified as safe for offline fallback")
	}
}

func TestOfflineFallbackDoesNotRunAfterDaemonApplicationError(t *testing.T) {
	storagePath := prepareBindUnixStorage(t)
	done := startBindUnixResponder(t, storagePath, func(conn net.Conn, _ protocol.UnixRequest) {
		writeBindUnixTestResponse(conn, nil, errors.New("daemon rejected mutation"))
	})

	var offlineCalls atomic.Int32
	result := dispatchUnixLocalOrOffline(
		"service_add",
		map[string]string{"name": "must-not-fallback"},
		func(*server.Server) (any, error) { return nil, errors.New("unexpected local call") },
		func() (any, error) {
			offlineCalls.Add(1)
			return "offline", nil
		},
	)
	<-done

	if !IsBindError(result) || !strings.Contains(ParseBindError(result), "daemon rejected mutation") {
		t.Fatalf("result = %s, want daemon application error", result)
	}
	if got := offlineCalls.Load(); got != 0 {
		t.Fatalf("offline fallback ran %d time(s) after daemon application error", got)
	}
}

func TestOfflineFallbackDoesNotRunAfterDaemonParseError(t *testing.T) {
	storagePath := prepareBindUnixStorage(t)
	done := startBindUnixResponder(t, storagePath, func(conn net.Conn, _ protocol.UnixRequest) {
		_, _ = conn.Write([]byte(`{"success":`))
	})

	var offlineCalls atomic.Int32
	result := dispatchUnixLocalOrOffline(
		"service_add",
		map[string]string{"name": "must-not-fallback"},
		func(*server.Server) (any, error) { return nil, errors.New("unexpected local call") },
		func() (any, error) {
			offlineCalls.Add(1)
			return "offline", nil
		},
	)
	<-done

	if !IsBindError(result) || !strings.Contains(ParseBindError(result), "parse daemon response") {
		t.Fatalf("result = %s, want daemon parse error", result)
	}
	if got := offlineCalls.Load(); got != 0 {
		t.Fatalf("offline fallback ran %d time(s) after daemon parse error", got)
	}
}

func TestOfflineFallbackDoesNotRunAfterDaemonTimeout(t *testing.T) {
	storagePath := prepareBindUnixStorage(t)
	originalTimeout := unixResponseIdleTimeout
	unixResponseIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { unixResponseIdleTimeout = originalTimeout })

	done := startBindUnixResponder(t, storagePath, func(conn net.Conn, _ protocol.UnixRequest) {
		_, _ = io.Copy(io.Discard, conn)
	})

	var offlineCalls atomic.Int32
	result := dispatchUnixLocalOrOffline(
		"service_add",
		map[string]string{"name": "must-not-fallback"},
		func(*server.Server) (any, error) { return nil, errors.New("unexpected local call") },
		func() (any, error) {
			offlineCalls.Add(1)
			return "offline", nil
		},
	)
	<-done

	if !IsBindError(result) {
		t.Fatalf("result = %s, want daemon timeout error", result)
	}
	if got := offlineCalls.Load(); got != 0 {
		t.Fatalf("offline fallback ran %d time(s) after daemon timeout", got)
	}
}

func TestConfiglessCanonicalSocketIsTriedBeforeOfflineFallback(t *testing.T) {
	StopNode()
	storagePath := t.TempDir()
	SetStoragePath(storagePath)
	done := startBindUnixResponder(t, storagePath, func(conn net.Conn, _ protocol.UnixRequest) {
		writeBindUnixTestResponse(conn, "daemon", nil)
	})

	var offlineCalls atomic.Int32
	result := dispatchUnixLocalOrOffline(
		"service_detail",
		map[string]string{"name": "socket-service"},
		func(*server.Server) (any, error) { return nil, errors.New("unexpected local call") },
		func() (any, error) {
			offlineCalls.Add(1)
			return "offline", nil
		},
	)

	if result != `"daemon"` {
		t.Fatalf("result = %s, want configless daemon response", result)
	}
	if got := offlineCalls.Load(); got != 0 {
		t.Fatalf("offline fallback ran %d time(s) while canonical socket was listening", got)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("configless daemon did not finish request")
	}
}

func TestOnlineAndOfflineServiceOperationsShareCanonicalStorage(t *testing.T) {
	StopNode()
	t.Cleanup(StopNode)

	root := t.TempDir()
	requestedPath := filepath.Join(root, "requested")
	canonicalPath := filepath.Join(root, "canonical")
	if err := os.MkdirAll(requestedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonicalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := protocol.NodeConfig{ID: "canonical-node", StoragePath: canonicalPath}
	if err := protocol.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(requestedPath, "config.json"), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	done := startBindUnixResponder(t, canonicalPath, func(conn net.Conn, req protocol.UnixRequest) {
		name, localService, buildErr := compute.BuildLocalServiceFromArgs(
			req.Args["name"], req.Args["type"], req.Args["exec"], req.Args["desc"],
			req.Args["param"], req.Args["no-required"], req.Args["schema-file"],
		)
		if buildErr == nil {
			buildErr = compute.UpsertLocalService(canonicalPath, name, localService)
		}
		writeBindUnixTestResponse(conn, nil, buildErr)
	})

	SetStoragePath(requestedPath)
	if got := GetStoragePath(); got != canonicalPath {
		t.Fatalf("active storage = %q, want canonical %q", got, canonicalPath)
	}
	if result := AddService("online-service", "exec", "/bin/true", "", "", "", ""); IsBindError(result) {
		t.Fatalf("online add: %s", result)
	}
	<-done

	if result := AddService("offline-service", "exec", "/bin/true", "", "", "", ""); IsBindError(result) {
		t.Fatalf("offline add: %s", result)
	}
	services, err := compute.LoadServicesMap(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := services["online-service"]; !ok {
		t.Fatal("online service missing from canonical storage")
	}
	if _, ok := services["offline-service"]; !ok {
		t.Fatal("offline service missing from canonical storage")
	}
	if _, err := os.Stat(compute.ServicesFilePath(requestedPath)); !os.IsNotExist(err) {
		t.Fatalf("requested alias gained split services state: %v", err)
	}
	if got := GetLocalBlobPath("abc123"); got != filepath.Join(canonicalPath, "abc123") {
		t.Fatalf("offline CAS path = %q, want canonical path", got)
	}
}

func TestStorageIdentityIsAbsoluteAndResolvesExistingSymlinks(t *testing.T) {
	StopNode()
	root := t.TempDir()
	realStorage := filepath.Join(root, "real")
	if err := os.MkdirAll(realStorage, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realStorage, alias); err != nil {
		t.Fatal(err)
	}

	SetStoragePath(alias)
	if got := GetStoragePath(); got != realStorage {
		t.Fatalf("canonical storage = %q, want resolved absolute path %q", got, realStorage)
	}
}

func TestClonePipelineResultRemainsSchemaJSON(t *testing.T) {
	detail, ok := uischema.FindAction("service", "clone_pipeline")
	if !ok {
		t.Fatal("clone_pipeline action missing")
	}
	raw := `{"id":"source-custom","version":1,"steps":[]}`
	if got := formatActionResult(detail, map[string]string{"id": "source"}, raw); got != raw {
		t.Fatalf("clone result = %s, want raw schema JSON", got)
	}
}

func prepareBindUnixStorage(t *testing.T) string {
	t.Helper()
	StopNode()
	storagePath := t.TempDir()
	if err := protocol.SaveConfig(protocol.NodeConfig{
		ID:          "bind-dispatch-contract",
		StoragePath: storagePath,
	}); err != nil {
		t.Fatal(err)
	}
	SetStoragePath(storagePath)
	return storagePath
}

func startBindUnixResponder(
	t *testing.T,
	storagePath string,
	respond func(net.Conn, protocol.UnixRequest),
) <-chan struct{} {
	t.Helper()
	socketPath := protocol.UnixSockPath(storagePath)
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req protocol.UnixRequest
		if decodeErr := json.NewDecoder(conn).Decode(&req); decodeErr != nil {
			return
		}
		respond(conn, req)
		_ = listener.Close()
	}()
	return done
}

func writeBindUnixTestResponse(conn net.Conn, data any, actionErr error) {
	resp := protocol.UnixResponse{Success: actionErr == nil}
	if actionErr != nil {
		resp.Error = actionErr.Error()
	} else if data != nil {
		resp.Data, _ = json.Marshal(data)
	}
	_ = json.NewEncoder(conn).Encode(resp)
}
