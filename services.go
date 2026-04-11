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
