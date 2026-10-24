package protocol

import (
	"fmt"
)

// ValidatePrefixMigration checks whether an in-flight pipeline execution can safely
// migrate from currentSchema to updatedSchema based on prefix preservation invariants:
// (a) Prefix Identity: all completed step IDs must still exist in the updated schema
//     with identical configurations and in the completed order.
// (b) Output Port Satisfaction: any connection in the new schema originating from
//     a completed step must resolve to an existing key in Outputs[FromStep].
// (c) No Retroactive Dependencies: upcoming steps in the new schema cannot depend
//     on newly inserted steps located prior to the current execution index.
func ValidatePrefixMigration(
	completedSteps []string,
	outputs map[string]map[string]any,
	currentSchema, updatedSchema PipelineSchema,
) error {
	if currentSchema.ID != updatedSchema.ID {
		return fmt.Errorf("pipeline schema ID mismatch: current %q, updated %q", currentSchema.ID, updatedSchema.ID)
	}
	if updatedSchema.Deleted {
		return fmt.Errorf("cannot migrate pipeline '%s' to a deleted schema", updatedSchema.ID)
	}
	if updatedSchema.Version < currentSchema.Version {
		return fmt.Errorf("pipeline '%s' schema downgrade rejected: updated version %d is older than current %d",
			updatedSchema.ID, updatedSchema.Version, currentSchema.Version)
	}
	if len(updatedSchema.Steps) == 0 {
		return fmt.Errorf("updated pipeline '%s' must have at least one step", updatedSchema.ID)
	}
	if PipelineHasCycle(updatedSchema) {
		return fmt.Errorf("updated pipeline '%s' schema contains cycles", updatedSchema.ID)
	}

	completedCount := len(completedSteps)
	if len(updatedSchema.Steps) < completedCount {
		return fmt.Errorf("prefix identity violation: updated schema has %d steps, but %d steps already completed",
			len(updatedSchema.Steps), completedCount)
	}

	// Index current schema steps by ID for configuration comparison.
	currentStepMap := make(map[string]PipelineStep, len(currentSchema.Steps))
	for _, s := range currentSchema.Steps {
		currentStepMap[s.ID] = s
	}

	completedSet := make(map[string]bool, completedCount)
	PopulateMap(completedSet, completedSteps, true)

	// Invariant (a): Prefix Identity
	// All completed step IDs must exist in the updated schema at identical prefix positions
	// and in the exact completed order with identical configurations.
	for i, completedID := range completedSteps {
		updatedStep := updatedSchema.Steps[i]
		if updatedStep.ID != completedID {
			return fmt.Errorf("prefix identity violation: step at index %d is %q in updated schema, expected completed step %q",
				i, updatedStep.ID, completedID)
		}
		origStep, exists := currentStepMap[completedID]
		if !exists {
			return fmt.Errorf("prefix identity violation: completed step %q missing from current schema", completedID)
		}
		if updatedStep.Service != origStep.Service {
			return fmt.Errorf("prefix identity violation: completed step %q service modified from %q to %q",
				completedID, origStep.Service, updatedStep.Service)
		}
		if updatedStep.TargetNodeID != origStep.TargetNodeID {
			return fmt.Errorf("prefix identity violation: completed step %q target node modified from %q to %q",
				completedID, origStep.TargetNodeID, updatedStep.TargetNodeID)
		}
	}

	// Index updated step positions and check for duplicate step IDs.
	updatedStepPositions := make(map[string]int, len(updatedSchema.Steps))
	for pos, s := range updatedSchema.Steps {
		if s.ID == "" {
			return fmt.Errorf("step ID cannot be empty in updated schema")
		}
		if _, exists := updatedStepPositions[s.ID]; exists {
			return fmt.Errorf("duplicate step ID %q in updated schema", s.ID)
		}
		updatedStepPositions[s.ID] = pos
	}

	// Invariant (b) Output Port Satisfaction & Invariant (c) No Retroactive Dependencies
	for _, conn := range updatedSchema.Connections {
		if conn.FromStep == "$initial" || conn.FromStep == "" {
			continue
		}

		fromPos, fromExists := updatedStepPositions[conn.FromStep]
		if !fromExists {
			return fmt.Errorf("connection source step %q not found in updated schema", conn.FromStep)
		}
		toPos, toExists := updatedStepPositions[conn.ToStep]
		if !toExists {
			return fmt.Errorf("connection target step %q not found in updated schema", conn.ToStep)
		}
		if fromPos >= toPos {
			return fmt.Errorf("pipeline steps are not in topological order: connection [%s].%s -> [%s].%s requires step %q to appear before step %q",
				conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromStep, conn.ToStep)
		}

		// (b) Output Port Satisfaction: any connection originating from a completed step
		// must resolve to an existing key in Outputs[FromStep].
		if completedSet[conn.FromStep] {
			stepOutputs, ok := outputs[conn.FromStep]
			if !ok {
				return fmt.Errorf("output port satisfaction violation: completed step %q has no recorded outputs for connection [%s].%s -> [%s].%s",
					conn.FromStep, conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort)
			}
			if _, hasPort := stepOutputs[conn.FromPort]; !hasPort {
				return fmt.Errorf("output port satisfaction violation: connection [%s].%s -> [%s].%s requires output port %q from completed step %q which was not produced",
					conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromPort, conn.FromStep)
			}
		}

		// (c) No Retroactive Dependencies:
		// Any step executing at or before the current execution index (toPos <= completedCount)
		// cannot depend on a step that is unexecuted (!completedSet[conn.FromStep]).
		if !completedSet[conn.FromStep] && toPos <= completedCount {
			return fmt.Errorf("retroactive dependency violation: upcoming step %q at index %d depends on unexecuted step %q",
				conn.ToStep, toPos, conn.FromStep)
		}
	}

	return nil
}
