package unixclient

import (
	"encoding/json"
	"errors"
	"net"
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
