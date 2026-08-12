package protocol

import (
	"strings"
	"testing"
)

func TestPipelineHasCycle(t *testing.T) {
	t.Parallel()
	acyclic := PipelineSchema{
		ID: "ok",
		Steps: []PipelineStep{
			{ID: "a", Service: "sa"},
			{ID: "b", Service: "sb"},
		},
		Connections: []PipelineConnection{
			{FromStep: "$initial", FromPort: "in", ToStep: "a", ToPort: "in"},
			{FromStep: "a", FromPort: "out", ToStep: "b", ToPort: "in"},
		},
	}
	if PipelineHasCycle(acyclic) {
		t.Fatal("expected acyclic pipeline")
	}

	cyclic := PipelineSchema{
		ID: "bad",
		Steps: []PipelineStep{
			{ID: "a", Service: "sa"},
			{ID: "b", Service: "sb"},
		},
		Connections: []PipelineConnection{
			{FromStep: "a", FromPort: "out", ToStep: "b", ToPort: "in"},
			{FromStep: "b", FromPort: "out", ToStep: "a", ToPort: "in"},
		},
	}
	if !PipelineHasCycle(cyclic) {
		t.Fatal("expected cyclic pipeline")
	}
}

func TestValidatePipelineSchema(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (ServiceSchema, bool) {
		switch name {
		case "sa":
			return ServiceSchema{
				Name:       "sa",
				Parameters: map[string]ServiceParameter{"in": {Type: "string"}},
				Outputs:    map[string]ServiceParameter{"out": {Type: "string"}},
			}, true
		case "sb":
			return ServiceSchema{
				Name:       "sb",
				Parameters: map[string]ServiceParameter{"in": {Type: "string"}},
				Outputs:    map[string]ServiceParameter{"out": {Type: "string"}},
			}, true
		default:
			return ServiceSchema{}, false
		}
	}

	ok := PipelineSchema{
		ID: "pipe",
		Steps: []PipelineStep{
			{ID: "a", Service: "sa"},
			{ID: "b", Service: "sb"},
		},
		Connections: []PipelineConnection{
			{FromStep: "a", FromPort: "out", ToStep: "b", ToPort: "in"},
		},
	}
	if err := ValidatePipelineSchema(ok, lookup); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cyclic := ok
	cyclic.Connections = append(cyclic.Connections, PipelineConnection{
		FromStep: "b", FromPort: "out", ToStep: "a", ToPort: "in",
	})
	err := ValidatePipelineSchema(cyclic, lookup)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}

	mismatch := PipelineSchema{
		ID: "mismatch",
		Steps: []PipelineStep{
			{ID: "a", Service: "sa"},
			{ID: "b", Service: "sb"},
		},
		Connections: []PipelineConnection{
			{FromStep: "a", FromPort: "out", ToStep: "b", ToPort: "missing"},
		},
	}
	err = ValidatePipelineSchema(mismatch, lookup)
	if err == nil || !strings.Contains(err.Error(), "not a valid input") {
		t.Fatalf("expected invalid input port error, got %v", err)
	}
}

func TestValidatePipelineSchemaRejectsNonTopologicalStepOrder(t *testing.T) {
	t.Parallel()

	schema := PipelineSchema{
		ID:      "reverse-order",
		Version: 1,
		Steps: []PipelineStep{
			{ID: "consumer", Service: "consume"},
			{ID: "producer", Service: "produce"},
		},
		Connections: []PipelineConnection{
			{FromStep: "producer", FromPort: "out", ToStep: "consumer", ToPort: "in"},
		},
	}

	err := ValidatePipelineSchema(schema, nil)
	if err == nil || !strings.Contains(err.Error(), "topological") {
		t.Fatalf("expected deterministic topological-order error, got %v", err)
	}
}

func TestNormalizePipelineSchemaVersionDefaultsLegacyRevision(t *testing.T) {
	t.Parallel()
	original := PipelineSchema{
		ID:      "legacy",
		Steps:   []PipelineStep{{ID: "step"}},
		Version: 0,
	}
	normalized := NormalizePipelineSchemaVersion(original)
	if normalized.Version != 1 {
		t.Fatalf("normalized version = %d, want 1", normalized.Version)
	}
	normalized.Steps[0].ID = "changed"
	if original.Steps[0].ID != "step" {
		t.Fatal("normalization must preserve clone isolation")
	}
}

func TestValidatePipelineRevisionRejectsDowngradeAndConflict(t *testing.T) {
	t.Parallel()

	current := PipelineSchema{
		ID:      "versioned",
		Version: 2,
		Steps:   []PipelineStep{{ID: "current", Service: "service"}},
	}
	older := current
	older.Version = 1
	if err := ValidatePipelineRevision(current, older); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("expected downgrade error, got %v", err)
	}

	conflict := current
	conflict.Steps = []PipelineStep{{ID: "conflict", Service: "service"}}
	if err := ValidatePipelineRevision(current, conflict); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected same-version mismatch error, got %v", err)
	}

	if err := ValidatePipelineRevision(current, current); err != nil {
		t.Fatalf("identical revision must be idempotent: %v", err)
	}
}

func TestValidatePipelineRevisionMakesTombstonesMonotonic(t *testing.T) {
	t.Parallel()

	active := PipelineSchema{
		ID:      "removed",
		Version: 3,
		Steps:   []PipelineStep{{ID: "step", Service: "service"}},
	}
	tombstone := active
	tombstone.Deleted = true

	if err := ValidatePipelineRevision(active, tombstone); err != nil {
		t.Fatalf("same-version remove must supersede add: %v", err)
	}
	if err := ValidatePipelineRevision(tombstone, tombstone); err != nil {
		t.Fatalf("duplicate tombstone must be idempotent: %v", err)
	}
	if err := ValidatePipelineRevision(tombstone, active); err == nil || !strings.Contains(err.Error(), "resurrect") {
		t.Fatalf("equal-version add must not resurrect tombstone: %v", err)
	}

	resurrected := active
	resurrected.Version++
	if err := ValidatePipelineRevision(tombstone, resurrected); err != nil {
		t.Fatalf("newer add may supersede tombstone: %v", err)
	}
}

func TestPipelineContractDeepCopiesNestedState(t *testing.T) {
	t.Parallel()

	schema := PipelineSchema{
		ID:          "copy",
		Version:     1,
		Steps:       []PipelineStep{{ID: "step", Service: "service"}},
		Connections: []PipelineConnection{{FromStep: "$initial", FromPort: "in", ToStep: "step", ToPort: "in"}},
	}
	clonedSchema := ClonePipelineSchema(schema)
	clonedSchema.Steps[0].ID = "mutated"
	clonedSchema.Connections[0].ToPort = "mutated"
	if schema.Steps[0].ID != "step" || schema.Connections[0].ToPort != "in" {
		t.Fatal("pipeline schema clone shares slice storage")
	}

	state := &PipelineExecutionState{
		Outputs: map[string]map[string]any{
			"step": {"nested": map[string]any{"items": []any{"original"}}},
		},
		OutputProducers: map[string]string{"step": "node-a"},
		SelectedTargets: map[string]string{"step": "node-b"},
	}
	clonedState := ClonePipelineExecutionState(state)
	clonedState.Outputs["step"]["nested"].(map[string]any)["items"].([]any)[0] = "mutated"
	clonedState.OutputProducers["step"] = "node-b"
	clonedState.SelectedTargets["step"] = "node-c"
	if state.Outputs["step"]["nested"].(map[string]any)["items"].([]any)[0] != "original" {
		t.Fatal("pipeline execution clone shares nested output storage")
	}
	if state.OutputProducers["step"] != "node-a" {
		t.Fatal("pipeline execution clone shares producer map")
	}
	if state.SelectedTargets["step"] != "node-b" {
		t.Fatal("pipeline execution clone shares selected-target map")
	}
}
