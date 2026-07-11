package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"proxyma/internal/protocol"
)

type ParameterDetail struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

type ServiceDetail struct {
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	ProviderAddress      string            `json:"providerAddress"`
	RequiredPermissions  []string          `json:"requiredPermissions"`
	Parameters           []ParameterDetail `json:"parameters"`
}

type LocalService struct {
	Type   string                 `json:"type"`
	Exec   string                 `json:"exec,omitempty"`
	Schema protocol.ServiceSchema `json:"schema"`
}

// DiscoverServices returns active cluster services.
func DiscoverServices() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_discover", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list, err := s.LocalServiceDiscover()
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// GetServiceDetails gets metadata for a given service.
func GetServiceDetails(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return `{"error": "Node is not running"}`
	}

	_, addr, schema, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}

	var reqPermissions []string
	hasImageParam := false
	hasFileParam := false

	var params []ParameterDetail
	for pName, rules := range schema.Parameters {
		lower := strings.ToLower(pName)
		isImg := strings.Contains(lower, "image") || strings.Contains(lower, "img") || strings.Contains(lower, "photo")
		isFil := strings.Contains(lower, "file") || strings.Contains(lower, "path")

		if isImg {
			hasImageParam = true
		}
		if isFil {
			hasFileParam = true
		}

		desc := fmt.Sprintf("Provide a text value for %s.", pName)
		switch rules.Type {
		case "bool":
			desc = fmt.Sprintf("Toggle to enable or disable the %s option.", pName)
		case "int", "float":
			desc = fmt.Sprintf("Enter a numerical value for %s.", pName)
		default:
			if isImg {
				desc = fmt.Sprintf("Provide an image file path or capture a photo for %s.", pName)
			}
		}

		params = append(params, ParameterDetail{
			Name:        pName,
			Type:        rules.Type,
			Required:    rules.Required,
			Description: desc,
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
		ProviderAddress:     addr,
		RequiredPermissions: reqPermissions,
		Parameters:          params,
	}

	b, _ := json.Marshal(detail)
	return string(b)
}

// AddService registers a new service configuration locally.
func AddService(name, serviceType, exec, desc, param, noRequired, schemaFile string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

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
		if serviceType != "exec" && localService.Type == "" {
			localService.Type = serviceType
		}
	} else {
		serviceName = name
		schema := protocol.ServiceSchema{
			Name:        serviceName,
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
			Type:   serviceType,
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
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

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
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_run", map[string]string{
			"service": name,
			"payload": payloadJson,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	resp, err := s.LocalServiceRun(name, payloadJson)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// GetTaskStatus queries the status of a specific task.
func GetTaskStatus(taskID string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_status", map[string]string{
			"task_id": taskID,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	resp, ok := s.Compute.GetTaskResponse(taskID)
	if !ok {
		return `{"error": "task not found"}`
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// RunFileService uploads the local input file if necessary, runs the generic file service, and returns the result.
func RunFileService(serviceName string, inputPath string, outputName string, paramJson string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_run_file", map[string]string{
			"service": serviceName,
			"input":   inputPath,
			"output":  outputName,
			"param":   paramJson,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	resp, err := s.LocalServiceRunFile(serviceName, inputPath, outputName, paramJson)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, _ := json.Marshal(resp)
	return string(b)
}
