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
func GetServiceSchema(name string) string {
	return dispatchUnixOrLocal("service_detail", map[string]string{"name": name}, func(s *server.Server) (any, error) {
		schema, _, err := s.LocalServiceDetail(name)
		if err != nil {
			return nil, err
		}
		return schema, nil
	})
}

// resolveServiceSchema loads ServiceSchema + optional remote provider address.
func resolveServiceSchema(name string) (schema protocol.ServiceSchema, addr string, err error) {
	s := getSrv()
	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_detail", map[string]string{"name": name})
		if err != nil {
			return schema, "", err
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return schema, "", fmt.Errorf("invalid service detail response: %w", err)
		}
		return schema, "", nil
	}
	return s.LocalServiceDetail(name)
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
		desc := fmt.Sprintf("Provide a text value for %s.", pName)
		uiHint := rules.UIHint
		switch rules.Type {
		case "bool":
			desc = fmt.Sprintf("Toggle to enable or disable the %s option.", pName)
		case "int", "float":
			desc = fmt.Sprintf("Enter a numerical value for %s.", pName)
		case "file":
			hasFileParam = true
			uiHint = protocol.EffectiveUIHint(pName, rules)
			if uiHint == "image_picker" {
				hasImageParam = true
				desc = fmt.Sprintf("Provide an image file path or capture a photo for %s.", pName)
			} else {
				desc = fmt.Sprintf("Provide a file path or select a file for %s.", pName)
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

// RunService runs a task and waits up to 30s.
func RunService(name string, payloadJson string) string {
	return dispatchUnixOrLocal("service_run", map[string]string{
		"service": name,
		"payload": payloadJson,
	}, func(s *server.Server) (any, error) {
		return s.LocalServiceRun(name, payloadJson)
	})
}

type StreamEventListener interface {
	OnChunk(chunkJSON string)
	OnError(errMsg string)
	OnComplete()
}

// StreamService runs a streaming task notifying listener of chunks in real time.
func StreamService(name string, payloadJson string, listener StreamEventListener) string {
	s := getSrv()
	if s == nil {
		go func() {
			conn, err := DialUnix(appStorage)
			if err != nil {
				if listener != nil {
					listener.OnError(err.Error())
				}
				return
			}
			defer func() { _ = conn.Close() }()

			if err := WriteUnixRequest(conn, "service_stream", map[string]string{
				"service": name,
				"payload": payloadJson,
			}); err != nil {
				if listener != nil {
					listener.OnError(err.Error())
				}
				return
			}

			_ = ScanUnixNDJSON(conn, func(resp protocol.UnixResponse) bool {
				if !resp.Success {
					if listener != nil {
						listener.OnError(resp.Error)
					}
					return false
				}
				if listener != nil && resp.Data != nil {
					listener.OnChunk(string(resp.Data))
				}
				return true
			})
			if listener != nil {
				listener.OnComplete()
			}
		}()

		return `{"status": "streaming_started"}`
	}

	go func() {
		err := s.LocalServiceStreamRun(name, payloadJson, func(chunk map[string]any) {
			if listener != nil {
				b, _ := json.Marshal(chunk)
				listener.OnChunk(string(b))
			}
		})
		if err != nil {
			if listener != nil {
				listener.OnError(err.Error())
			}
		} else {
			if listener != nil {
				listener.OnComplete()
			}
		}
	}()

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
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return AddPipelineRaw(id, string(schemaBytes))
}

func registerPipelineJSON(id, schemaJSON string) string {
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Sprintf(`{"error": "invalid pipeline schema json: %s"}`, err.Error())
	}
	schema.ID = id
	normalizedJSON, _ := json.Marshal(schema)

	return dispatchUnixOrLocal("pipeline_add", map[string]string{
		"schema": string(normalizedJSON),
	}, func(s *server.Server) (any, error) {
		if err := s.LocalPipelineAdd(string(normalizedJSON)); err != nil {
			return nil, err
		}
		return map[string]string{"message": "Pipeline added successfully"}, nil
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
		return map[string]string{"message": "Pipeline schema is valid"}, nil
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
		return map[string]string{"message": "Pipeline removed successfully"}, nil
	})
}

// ListPipelines returns a list of registered pipelines.
func ListPipelines() string {
	return dispatchUnixOrLocal("pipeline_list", nil, func(s *server.Server) (any, error) {
		return s.LocalPipelineList(), nil
	})
}

// RunPipeline runs a pipeline task.
func RunPipeline(id string, payloadJson string) string {
	return dispatchUnixOrLocal("service_run", map[string]string{
		"service": id,
		"payload": payloadJson,
	}, func(s *server.Server) (any, error) {
		return s.LocalServiceRun(id, payloadJson)
	})
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
