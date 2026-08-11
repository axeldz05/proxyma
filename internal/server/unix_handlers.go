package server

import (
	"encoding/json"
	"fmt"
	"net"

	"proxyma/internal/protocol"
	"proxyma/shared/uischema"
)

// UnixActionHandler dispatches a unix IPC action.
// If Stream is set, it owns the connection (NDJSON) and Unary is unused.
type UnixActionHandler struct {
	Unary  func(s *Server, args map[string]string) (any, error)
	Stream func(s *Server, args map[string]string, c net.Conn)
}

// unixHandlers is the daemon SSOT dispatch table keyed by uischema UnixAction strings.
var unixHandlers = map[string]UnixActionHandler{}

// requireUnixArgs rejects empty required arguments (L1). The daemon re-checks even
// though bind validates against the Registry, because raw unix clients skip that pass.
func requireUnixArgs(args map[string]string, names ...string) error {
	for _, name := range names {
		if args[name] == "" {
			return fmt.Errorf("missing %s parameter", name)
		}
	}
	return nil
}

func init() {
	register := func(domain, action string, h UnixActionHandler) {
		unixHandlers[uischema.MustUnixAction(domain, action)] = h
	}

	register("storage", "sync", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return nil, s.announceAndSync()
		},
	})
	register("storage", "list", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return s.LocalVFSList(), nil
		},
	})
	register("storage", "upload", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalVFSUpload(args["name"], args["path"])
		},
	})
	register("storage", "subscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := requireUnixArgs(args, "name"); err != nil {
				return nil, err
			}
			return nil, s.LocalVFSSubscribe(args["name"], true)
		},
	})
	register("storage", "unsubscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := requireUnixArgs(args, "name"); err != nil {
				return nil, err
			}
			return nil, s.LocalVFSSubscribe(args["name"], false)
		},
	})
	register("storage", "delete", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := requireUnixArgs(args, "name"); err != nil {
				return nil, err
			}
			return nil, s.Storage.DeleteLocalFile(args["name"])
		},
	})
	register("storage", "purge", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := requireUnixArgs(args, "name"); err != nil {
				return nil, err
			}
			return nil, s.Storage.DeleteLocalCache(args["name"])
		},
	})
	register("storage", "open", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := requireUnixArgs(args, "name"); err != nil {
				return nil, err
			}
			return nil, s.FetchFileOnDemand(args["name"])
		},
	})

	register("service", "discover", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return s.LocalServiceDiscover()
		},
	})
	register("service", "detail", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			schema, _, err := s.LocalServiceDetail(args["name"])
			if err != nil {
				return nil, err
			}
			return schema, nil
		},
	})
	register("service", "add", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return s.LocalServiceAdd(
				args["name"], args["type"], args["exec"], args["desc"],
				args["param"], args["no-required"], args["schema-file"],
			)
		},
	})
	register("service", "remove", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return s.LocalServiceRemove(args["name"])
		},
	})
	register("service", "subscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := s.LocalServiceSubscribe(args["name"], true); err != nil {
				return nil, err
			}
			return nil, nil
		},
	})
	register("service", "unsubscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			if err := s.LocalServiceSubscribe(args["name"], false); err != nil {
				return nil, err
			}
			return nil, nil
		},
	})
	register("service", "stream", UnixActionHandler{
		Stream: func(s *Server, args map[string]string, c net.Conn) {
			err := s.LocalServiceStreamRun(args["service"], args["payload"], func(chunk map[string]any) {
				chunkBytes, _ := json.Marshal(chunk)
				writeUnixNDJSON(c, protocol.UnixResponse{Success: true, Data: chunkBytes})
			})
			if err != nil {
				writeUnixNDJSON(c, protocol.UnixResponse{Success: false, Error: err.Error()})
			}
		},
	})
	register("service", "run", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return s.LocalServiceRun(args["service"], args["payload"], args["strategy"])
		},
	})
	register("service", "status", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			taskID := args["task_id"]
			if taskID == "" {
				return s.Compute.GetAllTaskStatuses(), nil
			}
			r, ok := s.Compute.GetTaskResponse(taskID)
			if !ok {
				return nil, fmt.Errorf("task not found")
			}
			return r, nil
		},
	})

	register("cluster", "invite", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			token, _, err := s.LocalInviteGenerate(protocol.DefaultInviteMinutes)
			return token, err
		},
	})
	register("telemetry", "logs", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return s.LocalLogs(), nil
		},
	})
	register("telemetry", "stats", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return uischema.BandwidthStatsRows(s.LocalBandwidthStats()), nil
		},
	})
	register("peers", "list", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return s.LocalPeersList(), nil
		},
	})

	register("service", "add_pipeline", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalPipelineAdd(args["schema"])
		},
	})
	register("service", "validate_pipeline", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalPipelineValidate(args["schema"])
		},
	})
	register("service", "remove_pipeline", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalPipelineRemove(args["id"])
		},
	})
	register("service", "list_pipelines", UnixActionHandler{
		Unary: func(s *Server, _ map[string]string) (any, error) {
			return s.LocalPipelineList(), nil
		},
	})
	register("service", "get_pipeline", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return s.LocalPipelineGet(args["id"])
		},
	})
	register("service", "clone_pipeline", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return s.LocalPipelineClone(args["id"], args["new_id"], args["target_node"])
		},
	})
}

// CallUnixUnary invokes the registered unary handler for a unix IPC action (L2).
// Used by bind InvokeDomainAction so localFn bodies are not duplicated.
func CallUnixUnary(s *Server, unixAction string, args map[string]string) (any, error) {
	h, ok := unixHandlers[unixAction]
	if !ok {
		return nil, fmt.Errorf("unknown action: %s", unixAction)
	}
	if h.Unary == nil {
		return nil, fmt.Errorf("action %s is not a unary handler", unixAction)
	}
	return h.Unary(s, args)
}

// HasUnixUnary reports whether unixAction has a unary handler.
func HasUnixUnary(unixAction string) bool {
	h, ok := unixHandlers[unixAction]
	return ok && h.Unary != nil
}

// RegisteredUnixActions returns the set of unix action strings with handlers (for consistency tests).
func RegisteredUnixActions() map[string]struct{} {
	out := make(map[string]struct{}, len(unixHandlers))
	for k := range unixHandlers {
		out[k] = struct{}{}
	}
	return out
}
