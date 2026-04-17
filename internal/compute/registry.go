package compute

import (
	"errors"
	"fmt"
	"maps"
	"proxyma/internal/protocol"
	"slices"
)

var ErrServiceDuplicate = errors.New("service is already registered")

func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		services: make(map[string]registeredService),
	}
}

func (r *ServiceRegistry) GetHandler(serviceName string) (ServiceHandler, bool){
	r.mu.RLock()
	defer r.mu.RUnlock()
	service, exists := r.services[serviceName]
	if !exists {
		return nil, exists
	}
	return service.handler, true
}

func (r *ServiceRegistry) Register(schema protocol.ServiceSchema, handler ServiceHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.services[schema.Name]; exists {
		return fmt.Errorf("failed to register '%s': '%w'", schema.Name, ErrServiceDuplicate)
	}

	r.services[schema.Name] = registeredService{schema: schema, handler: handler}
	return nil
}

func (r *ServiceRegistry) Get(name string) (protocol.ServiceSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	service, exists := r.services[name]
	return service.schema, exists
}

func (r *ServiceRegistry) ListAll() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Collect(maps.Keys(r.services))
}

func (r *ServiceRegistry) ValidateRequest(req protocol.TaskRequest) error {
	schema, exists := r.Get(req.Service)
	if !exists {
		return fmt.Errorf("validation failed: service '%s' is not supported by this node", req.Service)
	}

	for paramName, paramRule := range schema.Parameters {
		inputValue, inputProvided := req.Payload[paramName]
		if paramRule.Required && !inputProvided {
			return fmt.Errorf("missing required parameter: '%s'", paramName)
		}
		if !inputProvided {
			continue
		}
		if err := validateType(paramName, inputValue, paramRule.Type); err != nil {
			return err
		}
	}

	return nil
}

func validateType(paramName string, value any, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("invalid type for parameter '%s': expected string", paramName)
		}
	case "bool":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("invalid type for parameter '%s': expected bool", paramName)
		}
	case "int":
		switch v := value.(type) {
		case int, int32, int64:
			return nil
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("invalid type for parameter '%s': expected int, got float", paramName)
			}
		default:
			return fmt.Errorf("invalid type for parameter '%s': expected int", paramName)
		}
	case "float":
		switch value.(type) {
		case float32, float64, int, int32, int64:
			return nil
		default:
			return fmt.Errorf("invalid type for parameter '%s': expected float", paramName)
		}
	default:
		return fmt.Errorf("unknown schema type '%s' for parameter '%s'", expectedType, paramName)
	}
	return nil
}
