package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

type LocalService struct {
	Type   string                 `json:"type"`
	Exec   string                 `json:"exec,omitempty"`
	Schema protocol.ServiceSchema `json:"schema"`
}

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
			"service", "add",
			"--name", "my-script",
			"--storage", tempDir,
			"--type", "script",
			"--exec", "python3 main.py",
			"--desc", "My test script",
			"--param", "param1:string, param2:bool",
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
			Name:        "my-grpc",
			Description: "grpc schema",
			Parameters: map[string]protocol.ServiceParameter{
				"token": {Type: "string", Required: true},
			},
		}
		schemaBytes, _ := json.Marshal(schema)
		schemaPath := filepath.Join(tempDir, "schema.json")
		err = os.WriteFile(schemaPath, schemaBytes, 0644)
		require.NoError(t, err)

		rootCmd.SetArgs([]string{
			"service", "add",
			"--name", "my-grpc",
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

	t.Run("Remove service", func(t *testing.T) {
		rootCmd.SetArgs([]string{
			"service", "remove",
			"--name", "my-script",
			"--storage", tempDir,
		})

		err := rootCmd.Execute()
		require.NoError(t, err)

		servicesFile := filepath.Join(tempDir, "services.json")
		data, err := os.ReadFile(servicesFile)
		require.NoError(t, err)

		var services map[string]LocalService
		err = json.Unmarshal(data, &services)
		require.NoError(t, err)

		_, exists := services["my-script"]
		require.False(t, exists)
	})
}

func TestServiceDaemonCmds(t *testing.T) {
	tempDir := t.TempDir()

	cfg := protocol.NodeConfig{
		ID:          "test-node",
		StoragePath: tempDir,
	}
	err := protocol.SaveConfig(cfg)
	require.NoError(t, err)

	t.Run("service discover", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "service_discover", req.Action)
			return []string{"hello-service", "goodbye-service"}, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"service", "discover", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("service run", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			if req.Action == "service_detail" {
				return protocol.ServiceSchema{
					Name: "hello-service",
				}, nil
			}
			require.Equal(t, "service_run", req.Action)
			require.Equal(t, "hello-service", req.Args["service"])
			require.Equal(t, `{"x":1}`, req.Args["payload"])
			return protocol.ServiceTaskResponse{
				TaskID:  "task_123",
				Service: "hello-service",
				Status:  "completed",
				Outputs: map[string]any{"res": "val"},
			}, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"service", "run", "--name", "hello-service", "--payload", `{"x":1}`, "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("service status", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "service_status", req.Action)
			require.Equal(t, "task_123", req.Args["task_id"])
			return protocol.ServiceTaskResponse{
				TaskID:  "task_123",
				Service: "hello-service",
				Status:  "completed",
				Outputs: map[string]any{"res": "val"},
			}, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"service", "status", "--task_id", "task_123", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})
}
