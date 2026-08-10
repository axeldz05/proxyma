package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *Server) listenUnixSocket() {
	sockPath := protocol.UnixSockPath(s.Config.StoragePath)
	_ = os.Remove(sockPath) // clean up old socket if it exists
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		s.Config.Logger.Error("Failed to listen on unix socket", "error", err)
		return
	}
	s.unixListener = l
	s.Config.Logger.Info("Listening for local commands on unix socket", "path", sockPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handleUnixConnection(conn)
	}
}

func writeUnixResponse(c net.Conn, respData any, actionErr error) {
	var unixResp protocol.UnixResponse
	if actionErr != nil {
		unixResp = protocol.UnixResponse{Success: false, Error: actionErr.Error()}
	} else {
		var raw json.RawMessage
		if respData != nil {
			raw, _ = json.Marshal(respData)
		}
		unixResp = protocol.UnixResponse{Success: true, Data: raw}
	}
	respBytes, _ := json.Marshal(unixResp)
	_, _ = c.Write(respBytes)
}

func writeUnixNDJSON(c net.Conn, resp protocol.UnixResponse) {
	_ = utils.WriteNDJSON(c, resp)
}

func (s *Server) handleUnixConnection(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	if err != nil {
		return
	}

	// Legacy 1-byte command compat
	if buf[0] == 1 {
		s.Config.Logger.Info("Sync triggered via legacy unix socket command")
		err = s.announceAndSync()
		if err != nil {
			s.Config.Logger.Error("Sync via legacy unix socket failed", "error", err)
			_, _ = c.Write([]byte{0})
		} else {
			_, _ = c.Write([]byte{1})
		}
		return
	}

	// JSON Request
	if buf[0] == '{' {
		reader := io.MultiReader(bytes.NewReader(buf), c)
		var req protocol.UnixRequest
		if err := json.NewDecoder(reader).Decode(&req); err != nil {
			writeUnixResponse(c, nil, fmt.Errorf("invalid JSON request: %w", err))
			return
		}

		h, ok := unixHandlers[req.Action]
		if !ok {
			writeUnixResponse(c, nil, fmt.Errorf("unknown action: %s", req.Action))
			return
		}
		if h.Stream != nil {
			h.Stream(s, req.Args, c)
			return
		}
		if h.Unary == nil {
			writeUnixResponse(c, nil, fmt.Errorf("action %s has no handler", req.Action))
			return
		}
		respData, actionErr := h.Unary(s, req.Args)
		writeUnixResponse(c, respData, actionErr)
	}
}
