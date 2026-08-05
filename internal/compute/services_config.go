package compute

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyma/internal/protocol"
)

// LoadServicesMap reads services.json (L1). Missing file yields an empty map.
func LoadServicesMap(storagePath string) (map[string]protocol.LocalService, error) {
	servicesFile := ServicesFilePath(storagePath)
	services := make(map[string]protocol.LocalService)
	data, err := os.ReadFile(servicesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return services, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// SaveServicesMap writes services.json (L1).
func SaveServicesMap(storagePath string, services map[string]protocol.LocalService) error {
	servicesFile := ServicesFilePath(storagePath)
	newData, err := json.MarshalIndent(services, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(servicesFile, newData, 0644)
}

// ServicesFilePath returns the path to services.json under storagePath.
func ServicesFilePath(storagePath string) string {
	return filepath.Join(storagePath, "services.json")
}

// BuildLocalServiceFromArgs builds a LocalService from CLI/bind arguments (L2).
func BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile string) (serviceName string, localService protocol.LocalService, err error) {
	if serviceType == "" {
		serviceType = string(protocol.ServiceTypeExec)
	}

	if strings.HasSuffix(name, ".json") || schemaFile != "" {
		fileToRead := name
		if schemaFile != "" {
			fileToRead = schemaFile
		}
		data, readErr := os.ReadFile(fileToRead)
		if readErr != nil {
			return "", localService, fmt.Errorf("couldn't read service file: %w", readErr)
		}
		if schemaFile != "" {
			var schema protocol.ServiceSchema
			if err := json.Unmarshal(data, &schema); err != nil {
				return "", localService, fmt.Errorf("invalid schema file format: %w", err)
			}
			localService.Schema = schema
			serviceName = name
			localService.Schema.Name = serviceName
		} else {
			if err := json.Unmarshal(data, &localService); err != nil {
				return "", localService, fmt.Errorf("invalid file format: %w", err)
			}
			serviceName = localService.Schema.Name
		}
		if serviceName == "" {
			return "", localService, fmt.Errorf("service name is missing in JSON schema")
		}
		if exec != "" {
			localService.Exec = exec
		}
		if serviceType != string(protocol.ServiceTypeExec) && localService.Type == "" {
			localService.Type = protocol.ServiceType(serviceType).Normalize()
		}
		return serviceName, localService, nil
	}

	serviceName = name
	schema := protocol.ServiceSchema{
		Name:        serviceName,
		Type:        protocol.ServiceType(serviceType).Normalize(),
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
				return "", localService, fmt.Errorf("invalid parameter format '%s'. Use name:type", p)
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
				UIHint:   protocol.InferUIHint(paramName, paramType),
			}
		}
	}

	localService = protocol.LocalService{
		Type:   protocol.ServiceType(serviceType).Normalize(),
		Exec:   exec,
		Schema: schema,
	}
	return serviceName, localService, nil
}

// UpsertLocalService adds/updates a service in services.json (L2).
func UpsertLocalService(storagePath string, serviceName string, localService protocol.LocalService) error {
	services, err := LoadServicesMap(storagePath)
	if err != nil {
		return err
	}
	services[serviceName] = localService
	return SaveServicesMap(storagePath, services)
}

// DeleteLocalService removes a service from services.json (L2).
func DeleteLocalService(storagePath, name string) error {
	services, err := LoadServicesMap(storagePath)
	if err != nil {
		return err
	}
	if _, exists := services[name]; !exists {
		return fmt.Errorf("service '%s' not found", name)
	}
	delete(services, name)
	return SaveServicesMap(storagePath, services)
}

// BuildHandler constructs a ServiceHandler from type + exec (L2).
func BuildHandler(serviceType protocol.ServiceType, exec string) (ServiceHandler, error) {
	t := serviceType.Normalize()
	switch t {
	case protocol.ServiceTypeScript, protocol.ServiceTypeExec:
		return BuildScriptHandler(exec), nil
	case protocol.ServiceTypeGRPC:
		return BuildGRPCHandler(exec, 10*time.Second), nil
	case protocol.ServiceTypeGRPCBidi, protocol.ServiceTypeBidi:
		if strings.HasPrefix(exec, "http://") || strings.HasPrefix(exec, "https://") {
			return BuildGRPCBidiHandler(exec, 30*time.Second), nil
		}
		return BuildScriptHandler(exec), nil
	default:
		return nil, fmt.Errorf("unknown service type: %s", serviceType)
	}
}
