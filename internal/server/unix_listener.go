package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

const maxUnixRequestBytes = 64 << 10

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

func writeUnixNDJSON(c net.Conn, resp protocol.UnixResponse) error {
	return utils.WriteNDJSON(c, resp)
}

func (s *Server) handleUnixConnection(c net.Conn) {
	defer func() { _ = c.Close() }()
	reader := &io.LimitedReader{R: c, N: maxUnixRequestBytes + 1}
	var req protocol.UnixRequest
	if err := json.NewDecoder(reader).Decode(&req); err != nil {
		if reader.N == 0 {
			writeUnixResponse(c, nil, fmt.Errorf("invalid JSON request: exceeds %d bytes", maxUnixRequestBytes))
			return
		}
		writeUnixResponse(c, nil, fmt.Errorf("invalid JSON request: %w", err))
		return
	}

	h, ok := unixHandlers[req.Action]
	if !ok {
		writeUnixResponse(c, nil, fmt.Errorf("unknown action: %s", req.Action))
		return
	}
	validatedArgs, err := validateUnixArgs(req.Action, req.Args)
	if err != nil {
		writeUnixResponse(c, nil, err)
		return
	}
	req.Args = validatedArgs
	if h.Stream != nil {
		streamVersion := 0
		for _, advertised := range req.StreamVersions {
			if advertised == protocol.ServiceStreamVersion {
				streamVersion = protocol.ServiceStreamVersion
				break
			}
		}
		streamCtx, cancel := s.contextWithServerLifetime(context.Background())
		defer cancel()
		go func() {
			var probe [1]byte
			_, _ = c.Read(probe[:])
			cancel()
		}()
		h.Stream(streamCtx, s, req.Args, streamVersion, c)
		return
	}
	if h.Unary == nil {
		writeUnixResponse(c, nil, fmt.Errorf("action %s has no handler", req.Action))
		return
	}
	respData, actionErr := h.Unary(s, req.Args)
	writeUnixResponse(c, respData, actionErr)
}
