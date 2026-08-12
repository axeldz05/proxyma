package protocol

import (
	"fmt"
	"sort"
	"strings"
)

// ValidatePipelineRevision rejects stale or conflicting replacement schemas.
func ValidatePipelineRevision(current, next PipelineSchema) error {
	if current.ID != next.ID {
		return fmt.Errorf("pipeline schema ID mismatch: current %q, incoming %q", current.ID, next.ID)
	}
	if next.Version < current.Version {
		return fmt.Errorf("pipeline '%s' schema downgrade rejected: incoming version %d is older than current version %d",
			next.ID, next.Version, current.Version)
	}
	if next.Version > current.Version {
		return nil
	}

	currentDefinition := current
	currentDefinition.Deleted = false
	nextDefinition := next
	nextDefinition.Deleted = false
	if PipelineSchemaHash(nextDefinition) != PipelineSchemaHash(currentDefinition) {
		return fmt.Errorf("pipeline '%s' schema mismatch at version %d", next.ID, next.Version)
	}
	if current.Deleted && !next.Deleted {
		return fmt.Errorf("pipeline '%s' cannot resurrect deleted version %d", next.ID, next.Version)
	}
	return nil
}

// PipelineHasCycle reports whether Connections form a cycle among steps (ignores $initial).
// Uses Kahn topological sort over the full step graph.
func PipelineHasCycle(schema PipelineSchema) bool {
	adj := make(map[string][]string, len(schema.Steps))
	inDegree := make(map[string]int, len(schema.Steps))
	for _, step := range schema.Steps {
		inDegree[step.ID] = 0
	}
	type edgeKey struct{ from, to string }
	seen := make(map[edgeKey]bool)
	for _, conn := range schema.Connections {
		if conn.FromStep == "$initial" || conn.FromStep == "" || conn.ToStep == "" {
			continue
		}
		if _, ok := inDegree[conn.FromStep]; !ok {
			continue
		}
		if _, ok := inDegree[conn.ToStep]; !ok {
			continue
		}
		key := edgeKey{conn.FromStep, conn.ToStep}
		if seen[key] {
			continue
		}
		seen[key] = true
		adj[conn.FromStep] = append(adj[conn.FromStep], conn.ToStep)
		inDegree[conn.ToStep]++
	}
	queue := make([]string, 0, len(inDegree))
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		visited++
		for _, m := range adj[n] {
			inDegree[m]--
			if inDegree[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	return visited < len(schema.Steps)
}

// ValidatePipelineSchema checks structural integrity and port/type compatibility.
// lookup soft-skips port checks when a service schema is unknown (ok=false).
func ValidatePipelineSchema(schema PipelineSchema, lookup func(string) (ServiceSchema, bool)) error {
	if schema.ID == "" {
		return ErrEmptyPipelineID
	}
	if len(schema.Steps) == 0 {
		return fmt.Errorf("pipeline must have at least one step")
	}

	stepServices := make(map[string]string)
	stepNodes := make(map[string]string)
	stepPositions := make(map[string]int)
	var stepIDs []string
	for position, step := range schema.Steps {
		if step.ID == "" {
			return fmt.Errorf("step ID cannot be empty")
		}
		if step.Service == "" {
			return fmt.Errorf("step '%s' service name cannot be empty", step.ID)
		}
		if _, exists := stepServices[step.ID]; exists {
			return fmt.Errorf("duplicate step ID found: '%s'. Each step in a pipeline must have a unique ID", step.ID)
		}
		stepServices[step.ID] = step.Service
		stepNodes[step.ID] = step.TargetNodeID
		stepPositions[step.ID] = position
		stepIDs = append(stepIDs, step.ID)
	}

	if PipelineHasCycle(schema) {
		return fmt.Errorf("pipeline contains a cycle in Connections")
	}

	if lookup == nil {
		lookup = func(string) (ServiceSchema, bool) { return ServiceSchema{}, false }
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

	for _, conn := range schema.Connections {
		fromStr := conn.FromStep
		toStr := conn.ToStep
		toNodeStr := ""
		if nodeID, ok := stepNodes[toStr]; ok && nodeID != "" {
			toNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
		}

		if conn.FromStep != "$initial" {
			if _, exists := stepServices[conn.FromStep]; !exists {
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: source step '%s' is not defined in pipeline steps %v",
					conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromStep, stepIDs)
			}
		}

		toService, exists := stepServices[conn.ToStep]
		if !exists {
			return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: target step '%s' is not defined in pipeline steps %v",
				conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.ToStep, stepIDs)
		}
		if conn.FromStep != "$initial" && stepPositions[conn.FromStep] >= stepPositions[conn.ToStep] {
			return fmt.Errorf("pipeline steps are not in topological order: connection [%s].%s ──► [%s].%s requires step '%s' to appear before step '%s'",
				conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromStep, conn.ToStep)
		}

		toSchema, toSchemaExists := lookup(toService)
		if toSchemaExists {
			param, hasParam := toSchema.Parameters[conn.ToPort]
			if !hasParam {
				validParams := formatParams(toSchema.Parameters)
				extraNote := ""
				if _, isOutput := toSchema.Outputs[conn.ToPort]; isOutput {
					extraNote = fmt.Sprintf(" (Note: '%s' is defined as an OUTPUT port for service '%s', not an input parameter!)", conn.ToPort, toService)
				}
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid input parameter for step '%s' (running service '%s'%s). Expected input parameters for service '%s': [%s]%s",
					fromStr, conn.FromPort, toStr, conn.ToPort, conn.ToPort, toStr, toService, toNodeStr, toService, validParams, extraNote)
			}

			if conn.FromStep != "$initial" {
				fromService := stepServices[conn.FromStep]
				fromNodeStr := ""
				if nodeID, ok := stepNodes[conn.FromStep]; ok && nodeID != "" {
					fromNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
				}
				fromSchema, fromSchemaExists := lookup(fromService)
				if fromSchemaExists {
					outParam, hasOutParam := fromSchema.Outputs[conn.FromPort]
					if !hasOutParam {
						if len(fromSchema.Outputs) > 0 {
							validOutputs := formatParams(fromSchema.Outputs)
							return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid output for step '%s' (running service '%s'%s). Available output ports for service '%s': [%s]",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, conn.FromStep, fromService, fromNodeStr, fromService, validOutputs)
						}
					} else {
						if outParam.Type != param.Type {
							return fmt.Errorf("type mismatch on connection link [%s].%s ──► [%s].%s: source port '%s' outputs type '%s' (service '%s'%s, step '%s'), but target port '%s' requires type '%s' (service '%s'%s, step '%s')",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, outParam.Type, fromService, fromNodeStr, conn.FromStep, conn.ToPort, param.Type, toService, toNodeStr, conn.ToStep)
						}
					}
				}
			}
		}
	}

	return nil
}
