package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

type LocalService = protocol.LocalService

func TestServiceAddCmd(t *testing.T) {
	resetRootCommandState(t)
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
		servicesFile := compute.ServicesFilePath(tempDir)
		data, err := os.ReadFile(servicesFile)
		require.NoError(t, err)

		var services map[string]LocalService
		err = json.Unmarshal(data, &services)
		require.NoError(t, err)

		svc, exists := services["my-script"]
		require.True(t, exists)
		require.Equal(t, protocol.ServiceTypeScript, svc.Type)
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

		servicesFile := compute.ServicesFilePath(tempDir)
		data, err := os.ReadFile(servicesFile)
		require.NoError(t, err)

		var services map[string]LocalService
		err = json.Unmarshal(data, &services)
		require.NoError(t, err)

		svc, exists := services["my-grpc"]
		require.True(t, exists)
		require.Equal(t, protocol.ServiceTypeGRPC, svc.Type)

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

		servicesFile := compute.ServicesFilePath(tempDir)
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
	resetRootCommandState(t)
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

func TestExecuteCLIServiceStreamReturnsErrorEnvelope(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	result := executeCLIServiceStream(
		"broken-stream",
		`{}`,
		func(_ string, _ string, listener proxyma_bind.StreamEventListener) string {
			listener.OnChunk(`{"n":1}`)
			listener.OnError("upstream exploded")
			return `{"status":"streaming_started"}`
		},
		&stdout,
		&stderr,
	)

	if !proxyma_bind.IsBindError(result) {
		t.Fatalf("result = %s, want bind error envelope", result)
	}
	if !strings.Contains(proxyma_bind.ParseBindError(result), "upstream exploded") {
		t.Fatalf("result = %s, want stream error", result)
	}
	if strings.Contains(result, "Streaming completed") {
		t.Fatalf("stream error became completion: %s", result)
	}
	if got := strings.TrimSpace(stdout.String()); got != `{"n":1}` {
		t.Fatalf("stdout = %q, want prior stream chunk", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stream listener wrote directly to stderr: %q", stderr.String())
	}
}

func resetRootCommandState(t *testing.T) {
	t.Helper()
	reset := func() {
		var resetCommand func(*cobra.Command)
		resetCommand = func(command *cobra.Command) {
			resetFlags := func(flag *pflag.Flag) {
				if err := flag.Value.Set(flag.DefValue); err != nil {
					t.Fatalf("reset flag %s: %v", flag.Name, err)
				}
				flag.Changed = false
			}
			command.Flags().VisitAll(resetFlags)
			command.PersistentFlags().VisitAll(resetFlags)
			for _, child := range command.Commands() {
				resetCommand(child)
			}
		}
		resetCommand(rootCmd)
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(os.Stdout)
		rootCmd.SetErr(os.Stderr)
	}
	reset()
	t.Cleanup(reset)
}

func TestExecuteRootWritesOneErrorAndReturnsNonzero(t *testing.T) {
	resetRootCommandState(t)

	rootCmd.SetArgs([]string{"--definitely-invalid"})
	var stderr bytes.Buffer
	if code := executeRoot(&stderr); code == 0 {
		t.Fatal("invalid CLI invocation returned zero")
	}
	if count := strings.Count(stderr.String(), "unknown flag"); count != 1 {
		t.Fatalf("stderr = %q, want exactly one error", stderr.String())
	}
}

func TestRunCommandSurfacesShutdownFailure(t *testing.T) {
	resetRootCommandState(t)
	originalStart := runStartNode
	originalStop := runStopNodeWithError
	originalWait := runWaitForSignal
	t.Cleanup(func() {
		runStartNode = originalStart
		runStopNodeWithError = originalStop
		runWaitForSignal = originalWait
	})
	runStartNode = func(string, bool) string { return "" }
	runStopNodeWithError = func() string {
		return proxyma_bind.BindErrorJSON(errors.New("injected shutdown failure"))
	}
	runWaitForSignal = func() {}

	rootCmd.SetArgs([]string{"run", "--storage", t.TempDir()})
	var stderr bytes.Buffer
	if code := executeRoot(&stderr); code == 0 {
		t.Fatal("run shutdown failure returned zero")
	}
	if !strings.Contains(stderr.String(), "injected shutdown failure") {
		t.Fatalf("stderr = %q, want shutdown failure", stderr.String())
	}
}

func TestClusterJoinFreshStorageUsesDefaultPort(t *testing.T) {
	resetRootCommandState(t)
	storagePath := t.TempDir()

	originalJoin := joinCluster
	t.Cleanup(func() { joinCluster = originalJoin })

	var gotStorage, gotToken, gotNodeID, gotPort string
	joinCluster = func(storagePath, token, nodeID, port string) string {
		gotStorage = storagePath
		gotToken = token
		gotNodeID = nodeID
		gotPort = port
		return ""
	}

	rootCmd.SetArgs([]string{
		"cluster", "join",
		"--storage", storagePath,
		"--token", "invite-token",
		"--node_id", "fresh-node",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("fresh cluster join: %v", err)
	}
	if gotStorage != storagePath || gotToken != "invite-token" || gotNodeID != "fresh-node" {
		t.Fatalf("join args = storage %q token %q node %q", gotStorage, gotToken, gotNodeID)
	}
	if gotPort != protocol.DefaultTCPPort {
		t.Fatalf("join port = %q, want default %q", gotPort, protocol.DefaultTCPPort)
	}
}

func TestRequireConfigGuidanceNamesClusterJoin(t *testing.T) {
	err := requireConfig(t.TempDir())
	if err == nil {
		t.Fatal("missing config unexpectedly accepted")
	}
	if !strings.Contains(err.Error(), "proxyma cluster join") {
		t.Fatalf("guidance = %q, want real cluster join command", err)
	}
	if strings.Contains(err.Error(), "'proxyma join'") {
		t.Fatalf("guidance names nonexistent command: %q", err)
	}
}

func TestClonePipelineRegistersEffectiveIDAndReturnsSchemaJSON(t *testing.T) {
	originalClone := clonePipelineSchemaJSON
	originalAdd := addPipelineRaw
	t.Cleanup(func() {
		clonePipelineSchemaJSON = originalClone
		addPipelineRaw = originalAdd
	})

	cloned := protocol.PipelineSchema{
		ID:      "source-custom",
		Version: 1,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "echo"}},
	}
	clonedJSONBytes, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	clonedJSON := string(clonedJSONBytes)
	clonePipelineSchemaJSON = func(id, newID, targetNode string) string {
		if id != "source" || newID != "" || targetNode != "$local" {
			t.Fatalf("clone args = %q %q %q", id, newID, targetNode)
		}
		return clonedJSON
	}

	var registeredID, registeredSchema string
	addPipelineRaw = func(id, schemaJSON string) string {
		registeredID = id
		registeredSchema = schemaJSON
		return proxyma_bind.BindMessageJSON("added")
	}

	got := cliEscapes["service.clone_pipeline"](map[string]string{
		"id":          "source",
		"target_node": "$local",
	})
	if got != clonedJSON {
		t.Fatalf("clone result = %s, want schema JSON %s", got, clonedJSON)
	}
	if registeredID != cloned.ID {
		t.Fatalf("registered ID = %q, want cloned effective ID %q", registeredID, cloned.ID)
	}
	if registeredSchema != clonedJSON {
		t.Fatalf("registered schema = %q, want clone result", registeredSchema)
	}
}
