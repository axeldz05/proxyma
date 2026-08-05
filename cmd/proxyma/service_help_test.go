package main

import (
	"encoding/json"
	"os"
	"testing"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func TestBuildSampleJSONPayload(t *testing.T) {
	schema := &protocol.ServiceSchema{
		Name: "test_service",
		Parameters: map[string]protocol.ServiceParameter{
			"input_path": {Type: "file", Required: true},
			"lang":       {Type: "string", Required: false, Default: "eng"},
			"count":      {Type: "int", Required: false, Default: "5"},
			"active":     {Type: "bool", Required: false},
		},
	}

	sampleJSON := BuildSampleJSONPayload(schema)
	require.NotEmpty(t, sampleJSON)

	var parsed map[string]any
	err := json.Unmarshal([]byte(sampleJSON), &parsed)
	require.NoError(t, err)

	require.Equal(t, "/path/to/input_file", parsed["input_path"])
	require.Equal(t, "eng", parsed["lang"])
	require.Equal(t, float64(5), parsed["count"])
	require.Equal(t, true, parsed["active"])
}

func TestValidateAndPrintServiceHelp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "proxyma_help_test_*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Pre-create services.json
	svcFile := compute.ServicesFilePath(tempDir)
	svcs := map[string]any{
		"ocr": map[string]any{
			"type": "exec",
			"exec": "/bin/true",
			"schema": protocol.ServiceSchema{
				Name:        "ocr",
				Description: "OCR Service Test",
				Parameters: map[string]protocol.ServiceParameter{
					"input_path": {Type: "string", Required: true},
				},
			},
		},
	}
	data, _ := json.Marshal(svcs)
	_ = os.WriteFile(svcFile, data, 0644)

	// Case 1: Missing required parameter
	handled, err := ValidateAndPrintServiceHelp(tempDir, "ocr", "", "run", false)
	require.True(t, handled)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required parameter(s)")

	// Case 2: Help explicitly requested
	handled, err = ValidateAndPrintServiceHelp(tempDir, "ocr", "", "run", true)
	require.True(t, handled)
	require.NoError(t, err)

	// Case 3: Valid payload provided
	validPayload := `{"input_path": "/tmp/test.pdf"}`
	handled, err = ValidateAndPrintServiceHelp(tempDir, "ocr", validPayload, "run", false)
	require.False(t, handled)
	require.NoError(t, err)

	// Case 4: Valid key-value inputs provided
	kvInputs := `input_path=/tmp/test.pdf`
	handled, err = ValidateAndPrintServiceHelp(tempDir, "ocr", kvInputs, "run", false)
	require.False(t, handled)
	require.NoError(t, err)
}

func TestParseInputsToJSON(t *testing.T) {
	// JSON string pass-through
	jsonStr := ParseInputsToJSON(`{"key": "val", "num": 10}`)
	require.Equal(t, `{"key": "val", "num": 10}`, jsonStr)

	// Key-Value parsing
	kvStr := ParseInputsToJSON("input_path=/tmp/doc.pdf, active=true, count=5, score=3.14")
	var parsed map[string]any
	err := json.Unmarshal([]byte(kvStr), &parsed)
	require.NoError(t, err)
	require.Equal(t, "/tmp/doc.pdf", parsed["input_path"])
	require.Equal(t, true, parsed["active"])
	require.Equal(t, float64(5), parsed["count"])
	require.Equal(t, 3.14, parsed["score"])
}
