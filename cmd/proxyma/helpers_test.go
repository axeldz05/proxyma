package proxyma

import (
	"encoding/json"
	"net"
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
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var req protocol.UnixRequest
		err = json.NewDecoder(conn).Decode(&req)
		if err != nil {
			respBytes, _ := json.Marshal(protocol.UnixResponse{Success: false, Error: err.Error()})
			_, _ = conn.Write(respBytes)
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
		_, _ = conn.Write(respBytes)
	}()

	return l
}
