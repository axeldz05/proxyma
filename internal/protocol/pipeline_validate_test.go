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
