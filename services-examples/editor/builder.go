package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"proxyma/internal/protocol"
)

type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Builder struct {
	ID          string
	Version     int
	Steps       map[string]protocol.PipelineStep
	Connections []protocol.PipelineConnection
	Layout      map[string]NodePosition
}

func NewBuilder(id string) *Builder {
	return &Builder{
		ID:          id,
		Version:     1,
		Steps:       make(map[string]protocol.PipelineStep),
		Connections: make([]protocol.PipelineConnection, 0),
		Layout:      make(map[string]NodePosition),
	}
}

func (b *Builder) AddStep(stepID, serviceName, targetNodeID string, x, y float64) error {
	if stepID == "" {
		return fmt.Errorf("step ID cannot be empty")
	}
	if serviceName == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if _, exists := b.Steps[stepID]; exists {
		return fmt.Errorf("step '%s' already exists", stepID)
	}
	b.Steps[stepID] = protocol.PipelineStep{
		ID:           stepID,
		Service:      serviceName,
		TargetNodeID: targetNodeID,
	}
	b.Layout[stepID] = NodePosition{X: x, Y: y}
	return nil
}

func (b *Builder) RemoveStep(stepID string) {
	delete(b.Steps, stepID)
	delete(b.Layout, stepID)
	newConns := make([]protocol.PipelineConnection, 0)
	for _, conn := range b.Connections {
		if conn.FromStep != stepID && conn.ToStep != stepID {
			newConns = append(newConns, conn)
		}
	}
	b.Connections = newConns
}

func (b *Builder) Connect(fromStep, fromPort, toStep, toPort string, services map[string]protocol.ServiceSchema) error {
	if fromStep != "$initial" {
		if _, exists := b.Steps[fromStep]; !exists {
			return fmt.Errorf("source step '%s' does not exist", fromStep)
		}
	}
	toSvcStep, exists := b.Steps[toStep]
	if !exists {
		return fmt.Errorf("target step '%s' does not exist", toStep)
	}

	toSvcSchema, hasToSvc := services[toSvcStep.Service]
	if !hasToSvc {
		return fmt.Errorf("target service '%s' is not registered on the daemon", toSvcStep.Service)
	}
	toParam, hasToParam := toSvcSchema.Parameters[toPort]
	if !hasToParam {
		return fmt.Errorf("service '%s' has no input parameter named '%s'", toSvcStep.Service, toPort)
	}

	if fromStep != "$initial" {
		fromSvcStep := b.Steps[fromStep]
		fromSvcSchema, hasFromSvc := services[fromSvcStep.Service]
		if hasFromSvc && len(fromSvcSchema.Outputs) > 0 {
			fromOut, hasFromOut := fromSvcSchema.Outputs[fromPort]
			if !hasFromOut {
				return fmt.Errorf("service '%s' has no output port named '%s'", fromSvcStep.Service, fromPort)
			}
			if fromOut.Type != toParam.Type {
				return fmt.Errorf("type mismatch: cannot connect output '%s' of type '%s' (service '%s') to input '%s' of type '%s' (service '%s')",
					fromPort, fromOut.Type, fromSvcStep.Service, toPort, toParam.Type, toSvcStep.Service)
			}
		}
	}

	if fromStep != "$initial" {
		temp := b.Export()
		temp.Connections = append(temp.Connections, protocol.PipelineConnection{
			FromStep: fromStep,
			FromPort: fromPort,
			ToStep:   toStep,
			ToPort:   toPort,
		})
		if protocol.PipelineHasCycle(temp) {
			return fmt.Errorf("cyclic dependency detected: connecting '%s' to '%s' creates a loop", fromStep, toStep)
		}
	}

	b.Connections = append(b.Connections, protocol.PipelineConnection{
		FromStep: fromStep,
		FromPort: fromPort,
		ToStep:   toStep,
		ToPort:   toPort,
	})
	return nil
}

func (b *Builder) Disconnect(fromStep, fromPort, toStep, toPort string) {
	newConns := make([]protocol.PipelineConnection, 0)
	for _, conn := range b.Connections {
		if conn.FromStep == fromStep && conn.FromPort == fromPort && conn.ToStep == toStep && conn.ToPort == toPort {
			continue
		}
		newConns = append(newConns, conn)
	}
	b.Connections = newConns
}

func (b *Builder) Export() protocol.PipelineSchema {
	stepsList := make([]protocol.PipelineStep, 0, len(b.Steps))
	for _, step := range b.Steps {
		stepsList = append(stepsList, step)
	}
	return protocol.PipelineSchema{
		ID:          b.ID,
		Version:     b.Version,
		Steps:       stepsList,
		Connections: b.Connections,
	}
}

func (b *Builder) Import(schema protocol.PipelineSchema) {
	b.ID = schema.ID
	if schema.Version > 0 {
		b.Version = schema.Version
	}
	b.Steps = make(map[string]protocol.PipelineStep)
	b.Connections = make([]protocol.PipelineConnection, 0)
	b.Layout = make(map[string]NodePosition)

	for i, step := range schema.Steps {
		b.Steps[step.ID] = step
		b.Layout[step.ID] = NodePosition{X: float64(i) * 150.0, Y: 100.0}
	}
	b.Connections = append(b.Connections, schema.Connections...)
}

func (b *Builder) LoadFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read schema file: %w", err)
	}
	var schema protocol.PipelineSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("invalid pipeline schema JSON: %w", err)
	}
	if schema.ID == "" {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		schema.ID = strings.TrimSuffix(base, ext)
	}
	b.Import(schema)
	return nil
}

func (b *Builder) SaveToFile(filePath string) error {
	schema := b.Export()
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

// ValidateLocal uses protocol.ValidatePipelineSchema (SSOT). Soft-skips port checks when services is empty.
func (b *Builder) ValidateLocal(services map[string]protocol.ServiceSchema) error {
	return protocol.ValidatePipelineSchema(b.Export(), func(name string) (protocol.ServiceSchema, bool) {
		if len(services) == 0 {
			return protocol.ServiceSchema{}, false
		}
		sc, ok := services[name]
		return sc, ok
	})
}
