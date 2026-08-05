package proxyma_bind

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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

type LocalService struct {
	Type   protocol.ServiceType   `json:"type"`
	Exec   string                 `json:"exec,omitempty"`
	Schema protocol.ServiceSchema `json:"schema"`
}

// DiscoverServices returns active cluster services.
func DiscoverServices() string {
	return dispatchUnixOrLocal("service_discover", nil, func(s *server.Server) (any, error) {
		return s.LocalServiceDiscover()
	})
}

// GetServiceDetails gets metadata for a given service.
func GetServiceDetails(name string) string {
	s := getSrv()

	var schema protocol.ServiceSchema
	var addr string
	var err error

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_detail", map[string]string{
			"name": name,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return fmt.Sprintf(`{"error": "invalid service detail response: %v"}`, err)
		}
	} else {
		var exists bool
		schema, exists = s.Compute.GetService(name)
		if !exists {
			_, addr, schema, err = s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error())
			}
		}
	}

	var reqPermissions []string
	hasFileParam := false
	hasImageParam := false

	var params []ParameterDetail
	for pName, rules := range schema.Parameters {
		desc := fmt.Sprintf("Provide a text value for %s.", pName)
		switch rules.Type {
		case "bool":
			desc = fmt.Sprintf("Toggle to enable or disable the %s option.", pName)
		case "int", "float":
			desc = fmt.Sprintf("Enter a numerical value for %s.", pName)
		case "file":
			hasFileParam = true
			lower := strings.ToLower(pName)
			isImg := strings.Contains(lower, "image") || strings.Contains(lower, "img") || strings.Contains(lower, "photo")
			if isImg {
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
	s := getSrv()
	var schema protocol.ServiceSchema

	if s != nil {
		var exists bool
		schema, exists = s.Compute.GetService(name)
		if !exists {
			return ""
		}
	} else {
		data, err := sendUnixSocketCommand(appStorage, "service_detail", map[string]string{
			"name": name,
		})
		if err != nil {
			return ""
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return ""
		}
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
	s := getSrv()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_add", map[string]string{
			"name":        name,
			"type":        serviceType,
			"exec":        exec,
			"desc":        desc,
			"param":       param,
			"no-required": noRequired,
			"schema-file": schemaFile,
		})
		if err == nil {
			return string(data)
		}
	}

	if s != nil {
		msg, err := s.LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return fmt.Sprintf(`{"message": %q}`, msg)
	}

	if serviceType == "" {
		serviceType = "exec"
	}

	servicesFile := filepath.Join(appStorage, "services.json")
	services := make(map[string]LocalService)

	if data, err := os.ReadFile(servicesFile); err == nil {
		_ = json.Unmarshal(data, &services)
	}

	var localService LocalService
	var serviceName string

	if strings.HasSuffix(name, ".json") || schemaFile != "" {
		fileToRead := name
		if schemaFile != "" {
			fileToRead = schemaFile
		}
		data, err := os.ReadFile(fileToRead)
		if err != nil {
			return fmt.Sprintf(`{"error": "couldn't read service file: %v"}`, err)
		}
		if schemaFile != "" {
			var schema protocol.ServiceSchema
			if err := json.Unmarshal(data, &schema); err != nil {
				return fmt.Sprintf(`{"error": "invalid schema file format: %v"}`, err)
			}
			localService.Schema = schema
			serviceName = name
			localService.Schema.Name = serviceName
		} else {
			if err := json.Unmarshal(data, &localService); err != nil {
				return fmt.Sprintf(`{"error": "invalid file format: %v"}`, err)
			}
			serviceName = localService.Schema.Name
		}
		if serviceName == "" {
			return `{"error": "service name is missing in JSON schema"}`
		}
		if exec != "" {
			localService.Exec = exec
		}
		if serviceType != string(protocol.ServiceTypeExec) && localService.Type == "" {
			localService.Type = protocol.ServiceType(serviceType)
		}
	} else {
		serviceName = name
		schema := protocol.ServiceSchema{
			Name:        serviceName,
			Type:        protocol.ServiceType(serviceType),
			Description: desc,
			Parameters:  make(map[string]protocol.ServiceParameter),
		}

		noReqMap := make(map[string]bool)
		if noRequired != "" {
			for _, p := range strings.Split(noRequired, ",") {
				noReqMap[strings.TrimSpace(p)] = true
			}
		}

		if param != "" {
			for _, p := range strings.Split(param, ",") {
				parts := strings.Split(p, ":")
				if len(parts) < 2 {
					return fmt.Sprintf(`{"error": "invalid parameter format '%s'. Use name:type"}`, p)
				}

				paramName := strings.TrimSpace(parts[0])
				paramType := strings.TrimSpace(parts[1])

				isRequired := true
				if strings.HasSuffix(paramName, "?") {
					paramName = strings.TrimSuffix(paramName, "?")
					isRequired = false
				} else if noReqMap[paramName] {
					isRequired = false
				}

				schema.Parameters[paramName] = protocol.ServiceParameter{
					Type:     paramType,
					Required: isRequired,
				}
			}
		}

		localService = LocalService{
			Type:   protocol.ServiceType(serviceType),
			Exec:   exec,
			Schema: schema,
		}
	}

	services[serviceName] = localService

	newData, _ := json.MarshalIndent(services, "", "  ")
	if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
		return fmt.Sprintf(`{"error": "error saving services file: %v"}`, err)
	}
	return fmt.Sprintf(`{"message": "Service '%s' added successfully. Restart the node to apply changes."}`, serviceName)
}

// RemoveService deletes a service configuration locally.
func RemoveService(name string) string {
	s := getSrv()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_remove", map[string]string{
			"name": name,
		})
		if err == nil {
			return string(data)
		}
	}

	if s != nil {
		msg, err := s.LocalServiceRemove(name)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return fmt.Sprintf(`{"message": %q}`, msg)
	}

	servicesFile := filepath.Join(appStorage, "services.json")
	services := make(map[string]LocalService)

	if data, err := os.ReadFile(servicesFile); err == nil {
		_ = json.Unmarshal(data, &services)
	}

	if _, exists := services[name]; !exists {
		return fmt.Sprintf(`{"error": "service '%s' not found"}`, name)
	}

	delete(services, name)

	newData, _ := json.MarshalIndent(services, "", "  ")
	if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
		return fmt.Sprintf(`{"error": "error saving services file: %v"}`, err)
	}
	return fmt.Sprintf(`{"message": "Service '%s' removed successfully. Restart the node to apply changes."}`, name)
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
			cfg, err := protocol.LoadConfig(appStorage)
			if err != nil {
				if listener != nil {
					listener.OnError(err.Error())
				}
				return
			}
			sockPath := filepath.Join(cfg.StoragePath, "proxyma.sock")
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				if listener != nil {
					listener.OnError(fmt.Sprintf("daemon is unreachable: %v", err))
				}
				return
			}
			defer func() { _ = conn.Close() }()

			req := protocol.UnixRequest{
				Action: "service_stream",
				Args: map[string]string{
					"service": name,
					"payload": payloadJson,
				},
			}
			reqBytes, _ := json.Marshal(req)
			_, _ = conn.Write(reqBytes)

			scanner := bufio.NewScanner(conn)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				var resp protocol.UnixResponse
				if err := json.Unmarshal([]byte(line), &resp); err == nil {
					if !resp.Success {
						if listener != nil {
							listener.OnError(resp.Error)
						}
						return
					}
					if listener != nil && resp.Data != nil {
						listener.OnChunk(string(resp.Data))
					}
				}
			}
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
	s := getSrv()

	schemaBytes, err := os.ReadFile(schemaFile)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	var schema protocol.PipelineSchema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return fmt.Sprintf(`{"error": "invalid pipeline schema json: %s"}`, err.Error())
	}
	schema.ID = id

	schemaJSON, _ := json.Marshal(schema)

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "pipeline_add", map[string]string{
			"schema": string(schemaJSON),
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	err = s.LocalPipelineAdd(string(schemaJSON))
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return `{"message": "Pipeline added successfully"}`
}

// AddPipelineRaw registers a pipeline schema directly from its JSON string.
func AddPipelineRaw(id string, schemaJSON string) string {
	s := getSrv()

	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Sprintf(`{"error": "invalid pipeline schema json: %s"}`, err.Error())
	}
	schema.ID = id

	normalizedJSON, _ := json.Marshal(schema)

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "pipeline_add", map[string]string{
			"schema": string(normalizedJSON),
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	err := s.LocalPipelineAdd(string(normalizedJSON))
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return `{"message": "Pipeline added successfully"}`
}

// ValidatePipelineRaw validates a pipeline schema JSON string against the daemon without saving it.
func ValidatePipelineRaw(schemaJSON string) string {
	s := getSrv()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "pipeline_validate", map[string]string{
			"schema": schemaJSON,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	err := s.LocalPipelineValidate(schemaJSON)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return `{"message": "Pipeline schema is valid"}`
}


// RemovePipeline deletes a service pipeline schema.
func RemovePipeline(id string) string {
	s := getSrv()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "pipeline_remove", map[string]string{
			"id": id,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	err := s.LocalPipelineRemove(id)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return `{"message": "Pipeline removed successfully"}`
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
