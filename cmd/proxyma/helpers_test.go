package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func startMockUnixSocket(t *testing.T, dir string, handler func(req protocol.UnixRequest) (any, error)) net.Listener {
	sockPath := filepath.Join(dir, "proxyma.sock")
	l, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()

				var req protocol.UnixRequest
				err = json.NewDecoder(c).Decode(&req)
				if err != nil {
					respBytes, _ := json.Marshal(protocol.UnixResponse{Success: false, Error: err.Error()})
					_, _ = c.Write(respBytes)
					return
				}

				respData, err := handler(req)
				var resp protocol.UnixResponse
				if err != nil {
					resp = protocol.UnixResponse{Success: false, Error: err.Error()}
				} else {
					var raw json.RawMessage
					if respData != nil {
						raw, _ = json.Marshal(respData)
					}
					resp = protocol.UnixResponse{Success: true, Data: raw}
				}

				respBytes, _ := json.Marshal(resp)
				_, _ = c.Write(respBytes)
			}(conn)
		}
	}()

	return l
}

func TestEditPipelineResolvesEditorBinary(t *testing.T) {
	// Not parallel: uses t.Setenv for PROXYMA_EDITOR / PATH.

	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-editor")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	t.Setenv("PROXYMA_EDITOR", fake)
	path, err := resolveEditorBinary()
	require.NoError(t, err)
	require.Equal(t, fake, path)

	t.Setenv("PROXYMA_EDITOR", filepath.Join(dir, "missing"))
	_, err = resolveEditorBinary()
	require.Error(t, err)
	require.Contains(t, err.Error(), "PROXYMA_EDITOR")

	t.Setenv("PROXYMA_EDITOR", "")
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	onPath := filepath.Join(binDir, "proxyma-editor")
	require.NoError(t, os.WriteFile(onPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path, err = resolveEditorBinary()
	require.NoError(t, err)
	resolved, err := exec.LookPath("proxyma-editor")
	require.NoError(t, err)
	require.Equal(t, resolved, path)
}
