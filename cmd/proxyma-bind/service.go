package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"os"

	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/shared/uischema"
)

// ParameterDetail reuses the uischema SSOT parameter shape for Android DTOs.
type ParameterDetail = uischema.ParameterDetail

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
	return InvokeDomainAction("service", "discover", nil)
}

// GetServiceSchema returns the raw protocol.ServiceSchema JSON for a service (L2).
// Prefer this over GetServiceDetails when callers need Type/IsStreaming/Parameters map.
// Offline arm loads from services.json when daemon is unreachable.
func GetServiceSchema(name string) string {
	return InvokeDomainAction("service", "detail", map[string]string{"name": name})
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
	return InvokeDomainAction("service", "add", map[string]string{
		"name":        name,
		"type":        serviceType,
		"exec":        exec,
		"desc":        desc,
		"param":       param,
		"no-required": noRequired,
		"schema-file": schemaFile,
	})
}

// RemoveService deletes a service configuration locally.
func RemoveService(name string) string {
	return InvokeDomainAction("service", "remove", map[string]string{
		"name": name,
	})
}

// SubscribeService records interest in remote service notifies (name or pattern).
func SubscribeService(name string) string {
	return InvokeDomainAction("service", "subscribe", map[string]string{"name": name})
}

// UnsubscribeService drops a service interest pattern.
func UnsubscribeService(name string) string {
	return InvokeDomainAction("service", "unsubscribe", map[string]string{"name": name})
}

// RunService runs a task and waits up to 30s.
// Optional sortStrategy: fastest|cheapest|low_power (or canonical proxyma/strategy/* URNs).
func RunService(name string, payloadJson string, sortStrategy ...string) string {
	strategy := ""
	if len(sortStrategy) > 0 {
		strategy = sortStrategy[0]
	}
	return InvokeDomainAction("service", "run", map[string]string{
		"service":  name,
		"payload":  payloadJson,
		"strategy": strategy,
	})
}

type StreamEventListener interface {
	OnChunk(chunkJSON string)
	OnError(errMsg string)
	OnComplete()
}

// StreamService runs a streaming task notifying listener of chunks in real time.
func StreamService(name string, payloadJson string, listener StreamEventListener) string {
	detail, ok := uischema.FindAction("service", "stream")
	if !ok || detail.UnixAction == "" {
		return BindErrorJSON(fmt.Errorf("no unix action for service.stream"))
	}
	norm, err := NormalizeActionArgs("service", "stream", map[string]string{
		"name":    name,
		"payload": payloadJson,
	})
	if err != nil {
		return BindErrorJSON(err)
	}
	norm, err = uischema.ValidateActionArgs(detail, norm)
	if err != nil {
		return BindErrorJSON(err)
	}
	svc := norm["service"]
	payload := norm["payload"]
	dispatchUnixStreamOrLocal(
		detail.UnixAction,
		norm,
		func(s *server.Server, onChunk func(map[string]any)) error {
			return s.LocalServiceStreamRun(svc, payload, onChunk)
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
	return InvokeDomainAction("service", "status", map[string]string{
		"task_id": taskID,
	})
}

// AddPipeline registers a new service pipeline schema.
func AddPipeline(id string, schemaFile string) string {
	return InvokeDomainAction("service", "add_pipeline", map[string]string{
		"id":          id,
		"schema-file": schemaFile,
	})
}

// AddPipelineRaw registers a pipeline schema directly from its JSON string.
func AddPipelineRaw(id string, schemaJSON string) string {
	return InvokeDomainAction("service", "add_pipeline", map[string]string{
		"id":     id,
		"schema": schemaJSON,
	})
}

// ValidatePipelineRaw validates a pipeline schema JSON string against the daemon without saving it.
func ValidatePipelineRaw(schemaJSON string) string {
	return InvokeDomainAction("service", "validate_pipeline", map[string]string{
		"schema": schemaJSON,
	})
}

// RemovePipeline deletes a service pipeline schema.
func RemovePipeline(id string) string {
	return InvokeDomainAction("service", "remove_pipeline", map[string]string{
		"id": id,
	})
}

// ListPipelines returns a list of registered pipelines.
func ListPipelines() string {
	return InvokeDomainAction("service", "list_pipelines", nil)
}

// RunPipeline runs a pipeline task (alias of RunService).
func RunPipeline(id string, payloadJson string) string {
	return RunService(id, payloadJson)
}

// GetPipelineSchemaJson returns a pipeline schema JSON by ID.
func GetPipelineSchemaJson(id string) string {
	return InvokeDomainAction("service", "get_pipeline", map[string]string{
		"id": id,
	})
}

// ClonePipelineSchemaJson clones a pipeline schema, customizing ID and target node assignments.
func ClonePipelineSchemaJson(id string, newID string, targetNodeID string) string {
	return InvokeDomainAction("service", "clone_pipeline", map[string]string{
		"id":          id,
		"new_id":      newID,
		"target_node": targetNodeID,
	})
}
