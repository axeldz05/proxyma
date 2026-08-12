package server

import (
	"context"
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
	Stream func(ctx context.Context, s *Server, args map[string]string, streamVersion int, c net.Conn)
}

// unixHandlers is the daemon SSOT dispatch table keyed by uischema UnixAction strings.
var unixHandlers = map[string]UnixActionHandler{}

// validateUnixArgs applies the shared required/type/options contract for raw IPC.
func validateUnixArgs(unixAction string, args map[string]string) (map[string]string, error) {
	action, ok := uischema.FindUnixAction(unixAction)
	if !ok {
		return nil, fmt.Errorf("unknown action: %s", unixAction)
	}
	normalized, err := uischema.NormalizeActionArgs(action.Domain, action.Name, args)
	if err != nil {
		return nil, err
	}
	return uischema.ValidateActionArgs(action, normalized)
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
			return s.LocalVFSList()
		},
	})
	register("storage", "upload", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalVFSUpload(args["name"], args["path"])
		},
	})
	register("storage", "subscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalVFSSubscribe(args["name"], true)
		},
	})
	register("storage", "unsubscribe", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.LocalVFSSubscribe(args["name"], false)
		},
	})
	register("storage", "delete", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.Storage.DeleteLocalFile(args["name"])
		},
	})
	register("storage", "purge", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
			return nil, s.Storage.DeleteLocalCache(args["name"])
		},
	})
	register("storage", "open", UnixActionHandler{
		Unary: func(s *Server, args map[string]string) (any, error) {
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
		Stream: func(ctx context.Context, s *Server, args map[string]string, streamVersion int, c net.Conn) {
			err := s.LocalServiceStreamRunContext(ctx, args["service"], args["payload"], func(chunk map[string]any) error {
				chunkBytes, err := json.Marshal(chunk)
				if err != nil {
					return fmt.Errorf("marshal stream chunk: %w", err)
				}
				return writeUnixNDJSON(c, protocol.UnixResponse{
					Success:       true,
					Data:          chunkBytes,
					StreamVersion: streamVersion,
				})
			})
			if err != nil {
				_ = writeUnixNDJSON(c, protocol.UnixResponse{
					Success:       false,
					Error:         err.Error(),
					StreamVersion: streamVersion,
				})
				return
			}
			if streamVersion == protocol.ServiceStreamVersion {
				_ = writeUnixNDJSON(c, protocol.UnixResponse{
					Success:       true,
					Complete:      true,
					StreamVersion: streamVersion,
				})
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
	validatedArgs, err := validateUnixArgs(unixAction, args)
	if err != nil {
		return nil, err
	}
	return h.Unary(s, validatedArgs)
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
