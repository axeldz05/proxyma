package protocol

import (
	"testing"
)

func TestValidatePrefixMigration_UnitMatrix(t *testing.T) {
	currentSchema := PipelineSchema{
		ID:      "pipe-1",
		Version: 1,
		Steps: []PipelineStep{
			{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
			{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
		},
		Connections: []PipelineConnection{
			{FromStep: "$initial", FromPort: "data", ToStep: "step0", ToPort: "input"},
			{FromStep: "step0", FromPort: "output", ToStep: "step1", ToPort: "input"},
		},
	}

	completedSteps := []string{"step0"}
	validOutputs := map[string]map[string]any{
		"step0": {
			"output": "hello-world",
		},
	}

	t.Run("UpcomingStepAdded_Allowed", func(t *testing.T) {
		// Version 2 adds step2 after step1. step0 was completed. step1 and step2 are upcoming.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
				{ID: "step2", Service: "sink", TargetNodeID: "nodeC"},
			},
			Connections: []PipelineConnection{
				{FromStep: "$initial", FromPort: "data", ToStep: "step0", ToPort: "input"},
				{FromStep: "step0", FromPort: "output", ToStep: "step1", ToPort: "input"},
				{FromStep: "step1", FromPort: "result", ToStep: "step2", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err != nil {
			t.Fatalf("expected allowed migration, got error: %v", err)
		}
	})

	t.Run("UpcomingStepModified_Allowed", func(t *testing.T) {
		// Version 2 modifies step1's service and target node (which is upcoming). step0 is unchanged.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "fast-transform", TargetNodeID: "nodeD"},
			},
			Connections: []PipelineConnection{
				{FromStep: "$initial", FromPort: "data", ToStep: "step0", ToPort: "input"},
				{FromStep: "step0", FromPort: "output", ToStep: "step1", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err != nil {
			t.Fatalf("expected allowed modification of upcoming step, got: %v", err)
		}
	})

	t.Run("CompletedStepModified_Service_Rejected", func(t *testing.T) {
		// step0 was completed, but updated schema changed its service from echo to whisper.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "whisper", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: currentSchema.Connections,
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when completed step service is modified, got nil")
		}
	})

	t.Run("CompletedStepModified_TargetNode_Rejected", func(t *testing.T) {
		// step0 was completed on nodeA, but updated schema specifies nodeX.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeX"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: currentSchema.Connections,
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when completed step target node is modified, got nil")
		}
	})

	t.Run("CompletedStepRemoved_Rejected", func(t *testing.T) {
		// step0 was completed, but updated schema removed it completely.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: []PipelineConnection{
				{FromStep: "$initial", FromPort: "data", ToStep: "step1", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when completed step is removed, got nil")
		}
	})

	t.Run("PrefixOrderViolated_NewStepPrepended_Rejected", func(t *testing.T) {
		// Inserted step_new before step0, shifting step0 to index 1.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step_new", Service: "prep", TargetNodeID: "nodeA"},
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: []PipelineConnection{
				{FromStep: "step0", FromPort: "output", ToStep: "step1", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when completed prefix order is violated, got nil")
		}
	})

	t.Run("DependencyOnUnexecutedStep_Rejected", func(t *testing.T) {
		// step1 is the current step (about to execute).
		// updatedSchema introduces a connection to step1 from step2, which is an unexecuted step.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
				{ID: "step2", Service: "side", TargetNodeID: "nodeC"},
			},
			Connections: []PipelineConnection{
				{FromStep: "step0", FromPort: "output", ToStep: "step1", ToPort: "input"},
				{FromStep: "step2", FromPort: "out", ToStep: "step1", ToPort: "extra"}, // reverse / unexecuted dependency
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when upcoming step depends on unexecuted step, got nil")
		}
	})

	t.Run("OutputPortSatisfaction_PortMissing_Rejected", func(t *testing.T) {
		// Connection requires step0.missing_port, but step0 only produced step0.output.
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: []PipelineConnection{
				{FromStep: "step0", FromPort: "missing_port", ToStep: "step1", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when required output port was not produced by completed step, got nil")
		}
	})

	t.Run("OutputPortSatisfaction_MissingStepOutputs_Rejected", func(t *testing.T) {
		emptyOutputs := map[string]map[string]any{}
		updatedSchema := currentSchema
		updatedSchema.Version = 2

		err := ValidatePrefixMigration(completedSteps, emptyOutputs, currentSchema, updatedSchema)
		if err == nil {
			t.Fatal("expected error when completed step has no recorded outputs, got nil")
		}
	})

	t.Run("Downgrade_Rejected", func(t *testing.T) {
		downgradedSchema := currentSchema
		downgradedSchema.Version = 0

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, downgradedSchema)
		if err == nil {
			t.Fatal("expected error on version downgrade, got nil")
		}
	})

	t.Run("IDMismatch_Rejected", func(t *testing.T) {
		mismatchedSchema := currentSchema
		mismatchedSchema.ID = "other-pipeline"
		mismatchedSchema.Version = 2

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, mismatchedSchema)
		if err == nil {
			t.Fatal("expected error on pipeline ID mismatch, got nil")
		}
	})

	t.Run("CycleInUpdatedSchema_Rejected", func(t *testing.T) {
		cyclicSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
				{ID: "step2", Service: "loop", TargetNodeID: "nodeC"},
			},
			Connections: []PipelineConnection{
				{FromStep: "step1", FromPort: "out", ToStep: "step2", ToPort: "in"},
				{FromStep: "step2", FromPort: "out", ToStep: "step1", ToPort: "in"},
			},
		}

		err := ValidatePrefixMigration(completedSteps, validOutputs, currentSchema, cyclicSchema)
		if err == nil {
			t.Fatal("expected error when updated schema has cycle, got nil")
		}
	})

	t.Run("ZeroCompletedSteps_StartOfPipeline_Allowed", func(t *testing.T) {
		updatedSchema := PipelineSchema{
			ID:      "pipe-1",
			Version: 2,
			Steps: []PipelineStep{
				{ID: "step0_new", Service: "echo", TargetNodeID: "nodeA"},
				{ID: "step1", Service: "transform", TargetNodeID: "nodeB"},
			},
			Connections: []PipelineConnection{
				{FromStep: "$initial", FromPort: "data", ToStep: "step0_new", ToPort: "input"},
				{FromStep: "step0_new", FromPort: "output", ToStep: "step1", ToPort: "input"},
			},
		}

		err := ValidatePrefixMigration(nil, nil, currentSchema, updatedSchema)
		if err != nil {
			t.Fatalf("expected allowed migration when no steps have completed, got: %v", err)
		}
	})
}
