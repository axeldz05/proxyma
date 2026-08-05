package main

import (
	"testing"
)

func TestBuilderBasicOperations(t *testing.T) {
	b := NewBuilder("test-pipeline")

	if b.ID != "test-pipeline" {
		t.Errorf("Expected ID to be 'test-pipeline', got '%s'", b.ID)
	}

	// Add step
	err := b.AddStep("step1", "service-a", "node1", 10.0, 20.0)
	if err != nil {
		t.Fatalf("Unexpected error adding step: %v", err)
	}

	// Add duplicate step
	err = b.AddStep("step1", "service-b", "node1", 30.0, 40.0)
	if err == nil {
		t.Error("Expected error when adding duplicate step, got nil")
	}

	// Verify step contents
	step, exists := b.Steps["step1"]
	if !exists {
		t.Fatal("Expected step1 to exist")
	}
	if step.Service != "service-a" || step.TargetNodeID != "node1" {
		t.Errorf("Step content mismatch: %+v", step)
	}

	// Remove step
	b.RemoveStep("step1")
	if _, exists := b.Steps["step1"]; exists {
		t.Error("Expected step1 to be removed")
	}
}

func TestBuilderConnectValidation(t *testing.T) {
	b := NewBuilder("test-pipeline")
	_ = b.AddStep("s1", "svc-source", "node1", 0, 0)
	_ = b.AddStep("s2", "svc-target", "node1", 0, 0)

	// Mock service schemas registry
	services := map[string]ServiceSchema{
		"svc-source": {
			Name: "svc-source",
			Outputs: map[string]ServiceParameter{
				"out_str": {Type: "string"},
				"out_int": {Type: "int"},
			},
		},
		"svc-target": {
			Name: "svc-target",
			Parameters: map[string]ServiceParameter{
				"in_str": {Type: "string", Required: true},
				"in_int": {Type: "int", Required: true},
			},
		},
	}

	// Type matching connection
	err := b.Connect("s1", "out_str", "s2", "in_str", services)
	if err != nil {
		t.Errorf("Expected connection to succeed, got error: %v", err)
	}

	// Type mismatch connection
	err = b.Connect("s1", "out_str", "s2", "in_int", services)
	if err == nil {
		t.Error("Expected connection to fail due to type mismatch, got nil")
	}

	// Missing input parameter on target
	err = b.Connect("s1", "out_str", "s2", "in_invalid", services)
	if err == nil {
		t.Error("Expected connection to fail due to invalid target parameter, got nil")
	}

	// Missing output port on source
	err = b.Connect("s1", "out_invalid", "s2", "in_str", services)
	if err == nil {
		t.Error("Expected connection to fail due to invalid source output, got nil")
	}
}

func TestBuilderCycleDetection(t *testing.T) {
	b := NewBuilder("cycle-pipeline")
	_ = b.AddStep("s1", "svc-a", "", 0, 0)
	_ = b.AddStep("s2", "svc-b", "", 0, 0)
	_ = b.AddStep("s3", "svc-c", "", 0, 0)

	services := map[string]ServiceSchema{
		"svc-a": {
			Name: "svc-a",
			Parameters: map[string]ServiceParameter{"in": {Type: "string"}},
			Outputs:    map[string]ServiceParameter{"out": {Type: "string"}},
		},
		"svc-b": {
			Name: "svc-b",
			Parameters: map[string]ServiceParameter{"in": {Type: "string"}},
			Outputs:    map[string]ServiceParameter{"out": {Type: "string"}},
		},
		"svc-c": {
			Name: "svc-c",
			Parameters: map[string]ServiceParameter{"in": {Type: "string"}},
			Outputs:    map[string]ServiceParameter{"out": {Type: "string"}},
		},
	}

	// Setup line: s1 -> s2 -> s3
	if err := b.Connect("s1", "out", "s2", "in", services); err != nil {
		t.Fatalf("Connection s1->s2 failed: %v", err)
	}
	if err := b.Connect("s2", "out", "s3", "in", services); err != nil {
		t.Fatalf("Connection s2->s3 failed: %v", err)
	}

	// Adding connection s3 -> s1 should fail (creates a cycle: s1 -> s2 -> s3 -> s1)
	err := b.Connect("s3", "out", "s1", "in", services)
	if err == nil {
		t.Error("Expected cyclic connection s3->s1 to fail, got nil")
	}
}
