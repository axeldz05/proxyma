package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceAddCmd(t *testing.T) {
	tempDir := t.TempDir()
	
	// Create a mock config.json so LoadConfig doesn't fail
	cfg := protocol.NodeConfig{
		ID:          "test-node",
		StoragePath: tempDir,
	}
	err := protocol.SaveConfig(cfg)
	require.NoError(t, err)

	t.Run("Add service with explicit params", func(t *testing.T) {
		rootCmd.SetArgs([]string{
			"service", "add", "my-script",
			"--storage", tempDir,
			"--type", "script",
			"--exec", "python3 main.py",
			"--desc", "My test script",
			"--param", "param1: string, param2: bool",
			"--no-required", "param2",
		})

		err := rootCmd.Execute()
		require.NoError(t, err)

		// Verify services.json
		servicesFile := filepath.Join(tempDir, "services.json")
		data, err := os.ReadFile(servicesFile)
		require.NoError(t, err)

		var services map[string]LocalService
		err = json.Unmarshal(data, &services)
		require.NoError(t, err)

		svc, exists := services["my-script"]
		require.True(t, exists)
		require.Equal(t, "script", svc.Type)
		require.Equal(t, "python3 main.py", svc.Exec)
		require.Equal(t, "My test script", svc.Schema.Description)
		
		p1, ok := svc.Schema.Parameters["param1"]
		require.True(t, ok)
		require.Equal(t, "string", p1.Type)
		require.True(t, p1.Required)

		p2, ok := svc.Schema.Parameters["param2"]
		require.True(t, ok)
		require.Equal(t, "bool", p2.Type)
		require.False(t, p2.Required)
	})

	t.Run("Add service via schema file", func(t *testing.T) {
		schema := protocol.ServiceSchema{
			Name: "my-grpc",
			Description: "grpc schema",
			Parameters: map[string]protocol.ServiceParameter{
				"token": {Type: "string", Required: true},
			},
		}
		schemaBytes, _ := json.Marshal(schema)
		schemaPath := filepath.Join(tempDir, "schema.json")
		os.WriteFile(schemaPath, schemaBytes, 0644)

		rootCmd.SetArgs([]string{
			"service", "add", "my-grpc",
			"--storage", tempDir,
			"--type", "grpc", // Even if schema is provided, type/exec are outside schema
			"--schema-file", schemaPath,
		})

		err := rootCmd.Execute()
		require.NoError(t, err)

		servicesFile := filepath.Join(tempDir, "services.json")
		data, err := os.ReadFile(servicesFile)
		require.NoError(t, err)

		var services map[string]LocalService
		err = json.Unmarshal(data, &services)
		require.NoError(t, err)

		svc, exists := services["my-grpc"]
		require.True(t, exists)
		require.Equal(t, "grpc", svc.Type)
		
		tokenParam, ok := svc.Schema.Parameters["token"]
		require.True(t, ok)
		require.Equal(t, "string", tokenParam.Type)
		require.True(t, tokenParam.Required)
	})
}
