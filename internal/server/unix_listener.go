package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *Server) listenUnixSocket() {
	sockPath := filepath.Join(s.Config.StoragePath, "proxyma.sock")
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

		var respData any
		var actionErr error

		switch req.Action {
		case "sync":
			actionErr = s.announceAndSync()

		case "vfs_list":
			respData = s.LocalVFSList()

		case "vfs_upload":
			actionErr = s.LocalVFSUpload(req.Args["name"], req.Args["path"])

		case "vfs_subscribe", "vfs_unsubscribe", "vfs_delete", "vfs_purge", "vfs_fetch":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			switch req.Action {
			case "vfs_subscribe":
				actionErr = s.LocalVFSSubscribe(fileName, true)
			case "vfs_unsubscribe":
				actionErr = s.LocalVFSSubscribe(fileName, false)
			case "vfs_delete":
				actionErr = s.Storage.DeleteLocalFile(fileName)
			case "vfs_purge":
				actionErr = s.Storage.DeleteLocalCache(fileName)
			case "vfs_fetch":
				actionErr = s.FetchFileOnDemand(fileName)
			}

		case "service_discover":
			respData, actionErr = s.LocalServiceDiscover()

		case "service_detail":
			schema, _, err := s.LocalServiceDetail(req.Args["name"])
			if err != nil {
				actionErr = err
				break
			}
			respData = schema

		case "service_add":
			respData, actionErr = s.LocalServiceAdd(
				req.Args["name"],
				req.Args["type"],
				req.Args["exec"],
				req.Args["desc"],
				req.Args["param"],
				req.Args["no-required"],
				req.Args["schema-file"],
			)

		case "service_remove":
			respData, actionErr = s.LocalServiceRemove(req.Args["name"])

		case "service_stream":
			svcName := req.Args["service"]
			payloadStr := req.Args["payload"]
			err := s.LocalServiceStreamRun(svcName, payloadStr, func(chunk map[string]any) {
				chunkBytes, _ := json.Marshal(chunk)
				writeUnixNDJSON(c, protocol.UnixResponse{Success: true, Data: chunkBytes})
			})
			if err != nil {
				writeUnixNDJSON(c, protocol.UnixResponse{Success: false, Error: err.Error()})
			}
			return

		case "service_run":
			respData, actionErr = s.LocalServiceRun(req.Args["service"], req.Args["payload"])

		case "service_status":
			taskID := req.Args["task_id"]
			if taskID == "" {
				respData = s.Compute.GetAllTaskStatuses()
			} else {
				r, ok := s.Compute.GetTaskResponse(taskID)
				if !ok {
					actionErr = fmt.Errorf("task not found")
					break
				}
				respData = r
			}

		case "invite_generate":
			token, _, err := s.LocalInviteGenerate(DefaultInviteMinutes)
			respData, actionErr = token, err

		case "logs":
			respData = s.LocalLogs()

		case "bandwidth":
			respData = s.LocalBandwidthStats()

		case "peers":
			respData = s.LocalPeersList()

		case "pipeline_add":
			actionErr = s.LocalPipelineAdd(req.Args["schema"])

		case "pipeline_validate":
			actionErr = s.LocalPipelineValidate(req.Args["schema"])

		case "pipeline_remove":
			actionErr = s.LocalPipelineRemove(req.Args["id"])

		case "pipeline_list":
			respData = s.LocalPipelineList()

		case "pipeline_get":
			respData, actionErr = s.LocalPipelineGet(req.Args["id"])

		case "pipeline_clone":
			respData, actionErr = s.LocalPipelineClone(req.Args["id"], req.Args["new_id"], req.Args["target_node"])

		default:
			actionErr = fmt.Errorf("unknown action: %s", req.Action)
		}

		writeUnixResponse(c, respData, actionErr)
	}
}
