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

func (r *ServiceRegistry) GetHandler(serviceName string) (ServiceHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	service, exists := r.services[serviceName]
	if !exists {
		return nil, exists
	}
	return service.handler, true
}

func (r *ServiceRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services = make(map[string]registeredService)
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
	list := slices.Collect(maps.Keys(r.services))
	slices.Sort(list)
	return list
}

func (r *ServiceRegistry) ValidatePayload(serviceName string, payload map[string]any) error {
	schema, exists := r.Get(serviceName)
	if !exists {
		return fmt.Errorf("validation failed: service '%s' is not supported by this node", serviceName)
	}

	for paramName, paramRule := range schema.Parameters {
		inputValue, inputProvided := payload[paramName]
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

func (r *ServiceRegistry) ValidateRequest(req protocol.TaskRequest) error {
	return r.ValidatePayload(req.Service, req.Payload)
}

func validateType(paramName string, value any, expectedType string) error {
	return protocol.ServiceParameter{Type: expectedType}.ValidateValue(paramName, value)
}
