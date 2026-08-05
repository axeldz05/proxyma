package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PipelineStep struct {
	ID           string `json:"id"`
	Service      string `json:"service"`
	TargetNodeID string `json:"target_node_id,omitempty"`
}

type PipelineConnection struct {
	FromStep string `json:"from_step"`
	FromPort string `json:"from_port"`
	ToStep   string `json:"to_step"`
	ToPort   string `json:"to_port"`
}

type PipelineSchema struct {
	ID          string               `json:"id"`
	Version     int                  `json:"version"`
	Steps       []PipelineStep       `json:"steps"`
	Connections []PipelineConnection `json:"connections"`
}

type ServiceParameter struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type ServiceSchema struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Parameters  map[string]ServiceParameter `json:"parameters"`
	Outputs     map[string]ServiceParameter `json:"outputs,omitempty"`
}

type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Builder struct {
	ID          string
	Version     int
	Steps       map[string]PipelineStep
	Connections []PipelineConnection
	Layout      map[string]NodePosition
}

func NewBuilder(id string) *Builder {
	return &Builder{
		ID:          id,
		Version:     1,
		Steps:       make(map[string]PipelineStep),
		Connections: make([]PipelineConnection, 0),
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
	b.Steps[stepID] = PipelineStep{
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
	// Remove associated connections
	newConns := make([]PipelineConnection, 0)
	for _, conn := range b.Connections {
		if conn.FromStep != stepID && conn.ToStep != stepID {
			newConns = append(newConns, conn)
		}
	}
	b.Connections = newConns
}

func (b *Builder) Connect(fromStep, fromPort, toStep, toPort string, services map[string]ServiceSchema) error {
	// Validate steps exist
	if fromStep != "$initial" {
		if _, exists := b.Steps[fromStep]; !exists {
			return fmt.Errorf("source step '%s' does not exist", fromStep)
		}
	}
	toSvcStep, exists := b.Steps[toStep]
	if !exists {
		return fmt.Errorf("target step '%s' does not exist", toStep)
	}

	// Validate target service exists and has ToPort
	toSvcSchema, hasToSvc := services[toSvcStep.Service]
	if !hasToSvc {
		return fmt.Errorf("target service '%s' is not registered on the daemon", toSvcStep.Service)
	}
	toParam, hasToParam := toSvcSchema.Parameters[toPort]
	if !hasToParam {
		return fmt.Errorf("service '%s' has no input parameter named '%s'", toSvcStep.Service, toPort)
	}

	// Validate source service outputs if not initial and if outputs are defined
	if fromStep != "$initial" {
		fromSvcStep := b.Steps[fromStep]
		fromSvcSchema, hasFromSvc := services[fromSvcStep.Service]
		if hasFromSvc && len(fromSvcSchema.Outputs) > 0 {
			fromOut, hasFromOut := fromSvcSchema.Outputs[fromPort]
			if !hasFromOut {
				return fmt.Errorf("service '%s' has no output port named '%s'", fromSvcStep.Service, fromPort)
			}
			// Verify type compatibility
			if fromOut.Type != toParam.Type {
				return fmt.Errorf("type mismatch: cannot connect output '%s' of type '%s' (service '%s') to input '%s' of type '%s' (service '%s')",
					fromPort, fromOut.Type, fromSvcStep.Service, toPort, toParam.Type, toSvcStep.Service)
			}
		}
	}

	// Check for cycles (DAG)
	if fromStep != "$initial" {
		// Temporary add connection for pathfinding
		tempConns := append(b.Connections, PipelineConnection{
			FromStep: fromStep,
			FromPort: fromPort,
			ToStep:   toStep,
			ToPort:   toPort,
		})
		if hasCycle(fromStep, tempConns) {
			return fmt.Errorf("cyclic dependency detected: connecting '%s' to '%s' creates a loop", fromStep, toStep)
		}
	}

	// Add the connection
	b.Connections = append(b.Connections, PipelineConnection{
		FromStep: fromStep,
		FromPort: fromPort,
		ToStep:   toStep,
		ToPort:   toPort,
	})
	return nil
}

func (b *Builder) Disconnect(fromStep, fromPort, toStep, toPort string) {
	newConns := make([]PipelineConnection, 0)
	for _, conn := range b.Connections {
		if conn.FromStep == fromStep && conn.FromPort == fromPort && conn.ToStep == toStep && conn.ToPort == toPort {
			continue
		}
		newConns = append(newConns, conn)
	}
	b.Connections = newConns
}

func hasCycle(startNode string, connections []PipelineConnection) bool {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(node string) bool
	dfs = func(node string) bool {
		visited[node] = true
		recStack[node] = true

		for _, conn := range connections {
			if conn.FromStep == node {
				neighbor := conn.ToStep
				if !visited[neighbor] {
					if dfs(neighbor) {
						return true
					}
				} else if recStack[neighbor] {
					return true
				}
			}
		}

		recStack[node] = false
		return false
	}

	return dfs(startNode)
}

func (b *Builder) Export() PipelineSchema {
	stepsList := make([]PipelineStep, 0, len(b.Steps))
	for _, step := range b.Steps {
		stepsList = append(stepsList, step)
	}
	return PipelineSchema{
		ID:          b.ID,
		Version:     b.Version,
		Steps:       stepsList,
		Connections: b.Connections,
	}
}

func (b *Builder) Import(schema PipelineSchema) {
	b.ID = schema.ID
	if schema.Version > 0 {
		b.Version = schema.Version
	}
	b.Steps = make(map[string]PipelineStep)
	b.Connections = make([]PipelineConnection, 0)
	b.Layout = make(map[string]NodePosition)

	for i, step := range schema.Steps {
		b.Steps[step.ID] = step
		b.Layout[step.ID] = NodePosition{X: float64(i) * 150.0, Y: 100.0}
	}
	for _, conn := range schema.Connections {
		b.Connections = append(b.Connections, conn)
	}
}

func (b *Builder) LoadFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read schema file: %w", err)
	}
	var schema PipelineSchema
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

func (b *Builder) ValidateLocal(services map[string]ServiceSchema) error {
	if b.ID == "" {
		return fmt.Errorf("pipeline ID cannot be empty")
	}
	if len(b.Steps) == 0 {
		return fmt.Errorf("pipeline must have at least one step")
	}

	var stepIDs []string
	for id := range b.Steps {
		stepIDs = append(stepIDs, id)
	}

	formatParams := func(params map[string]ServiceParameter) string {
		if len(params) == 0 {
			return "none"
		}
		var list []string
		for k, p := range params {
			list = append(list, fmt.Sprintf("%s (%s)", k, p.Type))
		}
		sort.Strings(list)
		return strings.Join(list, ", ")
	}

	for _, conn := range b.Connections {
		fromStr := conn.FromStep
		toStr := conn.ToStep

		if conn.FromStep != "$initial" {
			if _, exists := b.Steps[conn.FromStep]; !exists {
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: source step '%s' is not defined in pipeline steps %v",
					fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromStep, stepIDs)
			}
		}
		toStep, exists := b.Steps[conn.ToStep]
		if !exists {
			return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: target step '%s' is not defined in pipeline steps %v",
				fromStr, conn.FromPort, toStr, conn.ToPort, conn.ToStep, stepIDs)
		}

		toNodeStr := ""
		if toStep.TargetNodeID != "" {
			toNodeStr = fmt.Sprintf(" on node '%s'", toStep.TargetNodeID)
		}

		if len(services) > 0 {
			toSvcSchema, hasToSvc := services[toStep.Service]
			if hasToSvc {
				toParam, hasToParam := toSvcSchema.Parameters[conn.ToPort]
				if !hasToParam {
					validParams := formatParams(toSvcSchema.Parameters)
					extraNote := ""
					if _, isOutput := toSvcSchema.Outputs[conn.ToPort]; isOutput {
						extraNote = fmt.Sprintf(" (Note: '%s' is defined as an OUTPUT port for service '%s', not an input parameter!)", conn.ToPort, toStep.Service)
					}
					return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid input parameter for step '%s' (running service '%s'%s). Expected input parameters for service '%s': [%s]%s",
						fromStr, conn.FromPort, toStr, conn.ToPort, conn.ToPort, toStr, toStep.Service, toNodeStr, toStep.Service, validParams, extraNote)
				}

				if conn.FromStep != "$initial" {
					fromStep := b.Steps[conn.FromStep]
					fromNodeStr := ""
					if fromStep.TargetNodeID != "" {
						fromNodeStr = fmt.Sprintf(" on node '%s'", fromStep.TargetNodeID)
					}
					fromSvcSchema, hasFromSvc := services[fromStep.Service]
					if hasFromSvc && len(fromSvcSchema.Outputs) > 0 {
						fromOut, hasFromOut := fromSvcSchema.Outputs[conn.FromPort]
						if !hasFromOut {
							validOutputs := formatParams(fromSvcSchema.Outputs)
							return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid output for step '%s' (running service '%s'%s). Available output ports for service '%s': [%s]",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, conn.FromStep, fromStep.Service, fromNodeStr, fromStep.Service, validOutputs)
						}
						if fromOut.Type != toParam.Type {
							return fmt.Errorf("type mismatch on connection link [%s].%s ──► [%s].%s: source port '%s' outputs type '%s' (service '%s'%s, step '%s'), but target port '%s' requires type '%s' (service '%s'%s, step '%s')",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, fromOut.Type, fromStep.Service, fromNodeStr, conn.FromStep, conn.ToPort, toParam.Type, toStep.Service, toNodeStr, conn.ToStep)
						}
					}
				}
			}
		}
	}

	for _, conn := range b.Connections {
		if conn.FromStep != "$initial" {
			if hasCycle(conn.FromStep, b.Connections) {
				return fmt.Errorf("cyclic dependency error: connection link [%s] ──► [%s] creates a closed loop in pipeline graph. Pipelines must be Directed Acyclic Graphs (DAGs)", conn.FromStep, conn.ToStep)
			}
		}
	}

	return nil
}
