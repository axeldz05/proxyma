package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"os"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

type ParameterDetail struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Required     bool     `json:"required"`
	Description  string   `json:"description"`
	DefaultValue string   `json:"defaultValue,omitempty"`
	Options      []string `json:"options,omitempty"`
	UIHint       string   `json:"uiHint,omitempty"`
}

type ServiceDetail struct {
	Name                string                               `json:"name"`
	Description         string                               `json:"description"`
	IsStreaming         bool                                 `json:"isStreaming,omitempty"`
	ProviderAddress     string                               `json:"providerAddress,omitempty"`
	RequiredPermissions []string                             `json:"requiredPermissions,omitempty"`
	Parameters          []ParameterDetail                    `json:"parameters"`
	Outputs             map[string]protocol.ServiceParameter `json:"outputs,omitempty"`
	UI                  *protocol.ServiceUIConfig            `json:"ui,omitempty"`
}

type LocalService = protocol.LocalService

// DiscoverServices returns active cluster services.
func DiscoverServices() string {
	return dispatchUnixOrLocal("service_discover", nil, func(s *server.Server) (any, error) {
		return s.LocalServiceDiscover()
	})
}

// GetServiceSchema returns the raw protocol.ServiceSchema JSON for a service (L2).
// Prefer this over GetServiceDetails when callers need Type/IsStreaming/Parameters map.
// Offline arm loads from services.json when daemon is unreachable.
func GetServiceSchema(name string) string {
	return dispatchUnixLocalOrOffline("service_detail", map[string]string{"name": name},
		func(s *server.Server) (any, error) {
			schema, _, err := s.LocalServiceDetail(name)
			if err != nil {
				return nil, err
			}
			return schema, nil
		},
		func() (any, error) {
			svcs, err := compute.LoadServicesMap(appStorage)
			if err != nil {
				return nil, err
			}
			svc, ok := svcs[name]
			if !ok {
				return nil, fmt.Errorf("service %q not found offline", name)
			}
			schema := protocol.NormalizeServiceSchema(name, svc.Schema, svc.Type)
			return schema, nil
		},
	)
}

// resolveServiceSchema loads ServiceSchema + optional remote provider address.
// When the in-process server is nil, delegates to GetServiceSchema (unix + offline arm).
func resolveServiceSchema(name string) (schema protocol.ServiceSchema, addr string, err error) {
	if s := getSrv(); s != nil {
		return s.LocalServiceDetail(name)
	}
	raw := GetServiceSchema(name)
	if IsBindError(raw) {
		return schema, "", fmt.Errorf("%s", ParseBindError(raw))
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return schema, "", fmt.Errorf("invalid service schema: %w", err)
	}
	return schema, "", nil
}

// LookupServiceSchema returns a typed ServiceSchema (unix + offline / in-process) (L2).
func LookupServiceSchema(name string) (protocol.ServiceSchema, error) {
	schema, _, err := resolveServiceSchema(name)
	if err != nil {
		return protocol.ServiceSchema{}, err
	}
	return protocol.NormalizeServiceSchema(name, schema, ""), nil
}

// GetServiceDetails gets Android-facing metadata for a given service (L3).
func GetServiceDetails(name string) string {
	schema, addr, err := resolveServiceSchema(name)
	if err != nil {
		return bindErrorJSON(err)
	}

	var reqPermissions []string
	hasFileParam := false
	hasImageParam := false

	var params []ParameterDetail
	for pName, rules := range schema.Parameters {
		desc, uiHint := protocol.DescribeParameter(pName, rules)
		if protocol.IsFilePickerHint(uiHint) || rules.Type == protocol.ParamTypeFile {
			hasFileParam = true
			if protocol.IsImagePickerHint(uiHint) {
				hasImageParam = true
			}
		}

		params = append(params, ParameterDetail{
			Name:         pName,
			Type:         rules.Type,
			Required:     rules.Required,
			Description:  desc,
			DefaultValue: rules.Default,
			Options:      rules.Options,
			UIHint:       uiHint,
		})
	}

	if hasImageParam {
		reqPermissions = append(reqPermissions, "Camera (to take photo for upload)")
		reqPermissions = append(reqPermissions, "Gallery / Storage (to select photo)")
	} else if hasFileParam {
		reqPermissions = append(reqPermissions, "Storage (to read/write local files)")
	}

	detail := ServiceDetail{
		Name:                schema.Name,
		Description:         schema.Description,
		IsStreaming:         schema.IsStreaming(),
		ProviderAddress:     addr,
		RequiredPermissions: reqPermissions,
		Parameters:          params,
		Outputs:             schema.Outputs,
		UI:                  schema.UI,
	}

	b, _ := json.Marshal(detail)
	return string(b)
}

// GetServiceUIContent retrieves HTML/JS content for a service's delegated UI if available.
func GetServiceUIContent(name string) string {
	schema, _, err := resolveServiceSchema(name)
	if err != nil {
		return ""
	}

	if schema.UI == nil {
		return ""
	}

	if schema.UI.LocalPath != "" {
		content, err := os.ReadFile(schema.UI.LocalPath)
		if err == nil {
			return string(content)
		}
	}

	return ""
}

// AddService registers a new service configuration locally.
func AddService(name, serviceType, exec, desc, param, noRequired, schemaFile string) string {
	return dispatchUnixLocalOrOffline("service_add", map[string]string{
		"name":        name,
		"type":        serviceType,
		"exec":        exec,
		"desc":        desc,
		"param":       param,
		"no-required": noRequired,
		"schema-file": schemaFile,
	}, func(s *server.Server) (any, error) {
		msg, err := s.LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile)
		if err != nil {
			return nil, err
		}
		return bindMessageJSON(msg), nil
	}, func() (any, error) {
		serviceName, localService, buildErr := compute.BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile)
		if buildErr != nil {
			return nil, buildErr
		}
		if saveErr := compute.UpsertLocalService(appStorage, serviceName, localService); saveErr != nil {
			return nil, fmt.Errorf("error saving services file: %w", saveErr)
		}
		return bindMessageJSON(fmt.Sprintf("Service '%s' added successfully. Restart the node to apply changes.", serviceName)), nil
	})
}

// RemoveService deletes a service configuration locally.
func RemoveService(name string) string {
	return dispatchUnixLocalOrOffline("service_remove", map[string]string{
		"name": name,
	}, func(s *server.Server) (any, error) {
		msg, err := s.LocalServiceRemove(name)
		if err != nil {
			return nil, err
		}
		return bindMessageJSON(msg), nil
	}, func() (any, error) {
		if delErr := compute.DeleteLocalService(appStorage, name); delErr != nil {
			return nil, delErr
		}
		return bindMessageJSON(fmt.Sprintf("Service '%s' removed successfully. Restart the node to apply changes.", name)), nil
	})
}

// SubscribeService records interest in remote service notifies (name or pattern).
func SubscribeService(name string) string {
	return dispatchUnixOrLocal("service_subscribe", map[string]string{"name": name}, func(s *server.Server) (any, error) {
		if err := s.LocalServiceSubscribe(name, true); err != nil {
			return nil, err
		}
		return bindMessageJSON(fmt.Sprintf("Subscribed to service pattern %q", name)), nil
	})
}

// UnsubscribeService drops a service interest pattern.
func UnsubscribeService(name string) string {
	return dispatchUnixOrLocal("service_unsubscribe", map[string]string{"name": name}, func(s *server.Server) (any, error) {
		if err := s.LocalServiceSubscribe(name, false); err != nil {
			return nil, err
		}
		return bindMessageJSON(fmt.Sprintf("Unsubscribed from service pattern %q", name)), nil
	})
}

// RunService runs a task and waits up to 30s.
// Optional sortStrategy: fastest|cheapest|low_power (or canonical proxyma/strategy/* URNs).
func RunService(name string, payloadJson string, sortStrategy ...string) string {
	strategy := ""
	if len(sortStrategy) > 0 {
		strategy = sortStrategy[0]
	}
	return dispatchUnixOrLocal("service_run", map[string]string{
		"service":  name,
		"payload":  payloadJson,
		"strategy": strategy,
	}, func(s *server.Server) (any, error) {
		return s.LocalServiceRun(name, payloadJson, strategy)
	})
}

type StreamEventListener interface {
	OnChunk(chunkJSON string)
	OnError(errMsg string)
	OnComplete()
}

// StreamService runs a streaming task notifying listener of chunks in real time.
func StreamService(name string, payloadJson string, listener StreamEventListener) string {
	dispatchUnixStreamOrLocal(
		"service_stream",
		map[string]string{"service": name, "payload": payloadJson},
		func(s *server.Server, onChunk func(map[string]any)) error {
			return s.LocalServiceStreamRun(name, payloadJson, onChunk)
		},
		func(chunkJSON string) {
			if listener != nil {
				listener.OnChunk(chunkJSON)
			}
		},
		func(errMsg string) {
			if listener != nil {
				listener.OnError(errMsg)
			}
		},
		func() {
			if listener != nil {
				listener.OnComplete()
			}
		},
	)
	return `{"status": "streaming_started"}`
}

// GetTaskStatus queries the status of a specific task.
func GetTaskStatus(taskID string) string {
	return dispatchUnixOrLocal("service_status", map[string]string{
		"task_id": taskID,
	}, func(s *server.Server) (any, error) {
		resp, ok := s.Compute.GetTaskResponse(taskID)
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		return resp, nil
	})
}

// AddPipeline registers a new service pipeline schema.
func AddPipeline(id string, schemaFile string) string {
	schemaBytes, err := os.ReadFile(schemaFile)
	if err != nil {
		return bindErrorJSON(err)
	}
	return AddPipelineRaw(id, string(schemaBytes))
}

func registerPipelineJSON(id, schemaJSON string) string {
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return bindErrorJSON(fmt.Errorf("invalid pipeline schema json: %s", err.Error()))
	}
	schema.ID = id
	normalizedJSON, _ := json.Marshal(schema)

	return dispatchUnixOrLocal("pipeline_add", map[string]string{
		"schema": string(normalizedJSON),
	}, func(s *server.Server) (any, error) {
		if err := s.LocalPipelineAdd(string(normalizedJSON)); err != nil {
			return nil, err
		}
		return bindMessageJSON("Pipeline added successfully"), nil
	})
}

// AddPipelineRaw registers a pipeline schema directly from its JSON string.
func AddPipelineRaw(id string, schemaJSON string) string {
	return registerPipelineJSON(id, schemaJSON)
}

// ValidatePipelineRaw validates a pipeline schema JSON string against the daemon without saving it.
func ValidatePipelineRaw(schemaJSON string) string {
	return dispatchUnixOrLocal("pipeline_validate", map[string]string{
		"schema": schemaJSON,
	}, func(s *server.Server) (any, error) {
		if err := s.LocalPipelineValidate(schemaJSON); err != nil {
			return nil, err
		}
		return bindMessageJSON("Pipeline schema is valid"), nil
	})
}

// RemovePipeline deletes a service pipeline schema.
func RemovePipeline(id string) string {
	return dispatchUnixOrLocal("pipeline_remove", map[string]string{
		"id": id,
	}, func(s *server.Server) (any, error) {
		if err := s.LocalPipelineRemove(id); err != nil {
			return nil, err
		}
		return bindMessageJSON("Pipeline removed successfully"), nil
	})
}

// ListPipelines returns a list of registered pipelines.
func ListPipelines() string {
	return dispatchUnixOrLocal("pipeline_list", nil, func(s *server.Server) (any, error) {
		return s.LocalPipelineList(), nil
	})
}

// RunPipeline runs a pipeline task (alias of RunService).
func RunPipeline(id string, payloadJson string) string {
	return RunService(id, payloadJson)
}

// GetPipelineSchemaJson returns a pipeline schema JSON by ID.
func GetPipelineSchemaJson(id string) string {
	return dispatchUnixOrLocal("pipeline_get", map[string]string{
		"id": id,
	}, func(s *server.Server) (any, error) {
		return s.LocalPipelineGet(id)
	})
}

// ClonePipelineSchemaJson clones a pipeline schema, customizing ID and target node assignments.
func ClonePipelineSchemaJson(id string, newID string, targetNodeID string) string {
	return dispatchUnixOrLocal("pipeline_clone", map[string]string{
		"id":          id,
		"new_id":      newID,
		"target_node": targetNodeID,
	}, func(s *server.Server) (any, error) {
		return s.LocalPipelineClone(id, newID, targetNodeID)
	})
}
