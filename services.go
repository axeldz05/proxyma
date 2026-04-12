package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

var ErrServiceDuplicate = errors.New("service is already registered")

func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		schemas: make(map[string]ServiceSchema),
	}
}

func (r *ServiceRegistry) Register(schema ServiceSchema) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.schemas[schema.Name]; exists {
		return fmt.Errorf("failed to register '%s': '%w'", schema.Name, ErrServiceDuplicate)
	}

	r.schemas[schema.Name] = schema
	return nil
}

func (r *ServiceRegistry) Get(name string) (ServiceSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	schema, exists := r.schemas[name]
	return schema, exists
}

func (r *ServiceRegistry) ListAll() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Collect(maps.Keys(r.schemas))
}

func (r *ServiceRegistry) ValidateRequest(req TaskRequest) error {
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
