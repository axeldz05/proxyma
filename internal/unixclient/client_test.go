package unixclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"proxyma/internal/protocol"
)

func TestUnavailableClassification(t *testing.T) {
	if !IsUnavailableDialError(syscall.ENOENT) || !IsUnavailableDialError(syscall.ECONNREFUSED) {
		t.Fatal("missing/refused socket must be classified as unavailable")
	}
	if IsUnavailableDialError(syscall.EACCES) {
		t.Fatal("permission failure must not be classified as unavailable")
	}
	wrapped := &unavailableError{err: errors.New("offline")}
	if !IsUnavailable(wrapped) {
		t.Fatal("wrapped unavailable error was not recognized")
	}
}

func TestUnaryPrimitivesRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		defer func() { _ = server.Close() }()
		buf := make([]byte, 4096)
		n, err := server.Read(buf)
		if err != nil {
			serverDone <- err
			return
		}
		var req protocol.UnixRequest
		if err := json.Unmarshal(buf[:n], &req); err != nil {
			serverDone <- err
			return
		}
		if req.Action != "peers" || req.Args["scope"] != "all" {
			serverDone <- errors.New("unexpected request")
			return
		}
		_, err = server.Write([]byte(`{"success":true,"data":{"ok":true}}`))
		serverDone <- err
	}()

	if err := WriteRequest(client, "peers", map[string]string{"scope": "all"}); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadResponseWithIdleTimeout(client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success || string(resp.Data) != `{"ok":true}` {
		t.Fatalf("response = %#v", resp)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestWriteStreamRequestAdvertisesStreamVersionV1(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		defer func() { _ = server.Close() }()
		buf := make([]byte, 4096)
		n, err := server.Read(buf)
		if err != nil {
			serverDone <- err
			return
		}
		var req protocol.UnixRequest
		if err := json.Unmarshal(buf[:n], &req); err != nil {
			serverDone <- err
			return
		}
		if len(req.StreamVersions) != 1 || req.StreamVersions[0] != protocol.ServiceStreamVersion {
			serverDone <- fmt.Errorf("stream versions = %v, want [%d]", req.StreamVersions, protocol.ServiceStreamVersion)
			return
		}
		serverDone <- nil
	}()

	if err := WriteStreamRequest(client, "service.run", map[string]string{"name": "lab.echo"}); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestScanNDJSONForwardsFramesAndStopsOnFalse(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		_ = server.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = server.Write([]byte(`{"success":true,"data":{"n":1}}` + "\n" + `{"success":true,"data":{"n":2}}` + "\n"))
	}()

	var seen []int
	err := ScanNDJSON(client, func(resp protocol.UnixResponse) bool {
		var payload struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(resp.Data, &payload)
		seen = append(seen, payload.N)
		return len(seen) < 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("frames = %v, want [1 2]", seen)
	}
}

func TestScanNDJSONRejectsMalformedChunkWithoutCallback(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		_ = server.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = server.Write([]byte("not-json\n"))
	}()

	called := false
	err := ScanNDJSON(client, func(protocol.UnixResponse) bool {
		called = true
		return true
	})
	if err == nil || !strings.Contains(err.Error(), "invalid NDJSON stream chunk") {
		t.Fatalf("err = %v, want invalid NDJSON stream chunk", err)
	}
	if called {
		t.Fatal("callback must not run for a malformed chunk")
	}
}

func TestReadResponseRejectsOversizedPayload(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		_ = server.SetWriteDeadline(time.Now().Add(10 * time.Second))
		chunk := bytes.Repeat([]byte("a"), 4096)
		for {
			if _, err := server.Write(chunk); err != nil {
				return
			}
		}
	}()

	_, err := ReadResponseWithIdleTimeout(client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "exceeds 32MB") {
		t.Fatalf("err = %v, want response cap error", err)
	}
}

func TestReadResponseRejectsInvalidJSON(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		_, _ = server.Write([]byte("not-json"))
	}()

	_, err := ReadResponseWithIdleTimeout(client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "failed to parse daemon response") {
		t.Fatalf("err = %v, want parse error", err)
	}
}

func TestCallUnaryPropagatesBusinessError(t *testing.T) {
	tmp := t.TempDir()
	listener, err := net.Listen("unix", protocol.UnixSockPath(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		var req protocol.UnixRequest
		if json.Unmarshal(buf[:n], &req) != nil || req.Action != "service.remove" {
			return
		}
		_, _ = conn.Write([]byte(`{"success":false,"error":"service 'lab.echo' not found"}`))
	}()

	_, err = CallUnary(tmp, "service.remove", map[string]string{"name": "lab.echo"})
	if err == nil || err.Error() != "service 'lab.echo' not found" {
		t.Fatalf("err = %v, want propagated business error", err)
	}
}

func TestDialClassifiesUnavailableAndHonorsConfigAlias(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := Dial(missing)
	if err == nil || !IsUnavailable(err) {
		t.Fatalf("err = %v, want unavailable for absent socket", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("permission classification is unreliable when running as root")
	}
	denied := filepath.Join(t.TempDir(), "denied")
	if err := os.MkdirAll(denied, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(denied, 0755) }()
	_, err = Dial(denied)
	if err == nil || IsUnavailable(err) {
		t.Fatalf("err = %v, permission failure must not be classified as unavailable", err)
	}

	alias := t.TempDir()
	real := t.TempDir()
	// SaveConfig always writes next to its own StoragePath, so the alias
	// config (alias/ -> real/) is written by hand to exercise Dial's redirect.
	aliasConfig := fmt.Sprintf(`{"id":"alias-node","storage_path":%q}`, real)
	if err := os.WriteFile(filepath.Join(alias, "config.json"), []byte(aliasConfig), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", protocol.UnixSockPath(real))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
		accepted <- struct{}{}
	}()

	conn, err := Dial(alias)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("dial via config alias never reached the aliased socket")
	}
}

func TestCanonicalFilesystemPath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := CanonicalFilesystemPath(real); got != real {
		t.Fatalf("fully existing path = %q, want %q", got, real)
	}
	withSuffix := filepath.Join(link, "missing", "sub")
	if got := CanonicalFilesystemPath(withSuffix); got != filepath.Join(real, "missing", "sub") {
		t.Fatalf("symlinked path with missing suffix = %q, want %q", got, filepath.Join(real, "missing", "sub"))
	}
	ghost := filepath.Join(base, "ghost", "node")
	if got := CanonicalFilesystemPath(ghost); got != ghost {
		t.Fatalf("nonexistent root = %q, want cleaned absolute %q", got, ghost)
	}
}
