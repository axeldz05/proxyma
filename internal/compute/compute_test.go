package compute_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setupMockExecutable(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "mock_script.go")
	binaryPath := filepath.Join(tempDir, "mock_bin")

	// The mock script reads JSON from stdin.
	// It uses the "test_scenario" field injected by the test to determine its behavior.
	sourceCode := `
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1)
	}

	var payload map[string]any
	if err := json.Unmarshal(inputBytes, &payload); err != nil {
		os.Exit(1)
	}

	scenario, _ := payload["test_scenario"].(string)

	switch scenario {
	case "success":
		// Echo the payload back with some modifications
		payload["status"] = "ok"
		payload["processed_by"] = "mock_binary"
		out, _ := json.Marshal(payload)
		fmt.Print(string(out))
		
	case "fail_execution":
		// Simulate a script crash or expected error
		fmt.Fprint(os.Stderr, "simulated script crash due to invalid state")
		os.Exit(1)
		
	case "invalid_json":
		// Simulate a script returning malformed data
		fmt.Print("{ this is not valid json ]")
		
	default:
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(sourcePath, []byte(sourceCode), 0644); err != nil {
		t.Fatalf("Failed to write mock source code: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to compile mock binary: %v", err)
	}

	return binaryPath
}


func TestCannotRegisterDuplicateServices(t *testing.T) {
    registry := compute.NewServiceRegistry() 
    
    schema := protocol.ServiceSchema{ Name: "ocr" }
	
	var mockHandler compute.ServiceHandler = func(context.Context, map[string]any) (map[string]any, error) {
        return map[string]any{}, nil
    }
    err1 := registry.Register(schema, mockHandler)
    err2 := registry.Register(schema, mockHandler)
    
    require.NoError(t, err1)
    require.ErrorIs(t, err2, compute.ErrServiceDuplicate)
}

func TestWorkerExecutesTaskAndStoresResult(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mockPeerClient := &testutil.MockPeerClient{}
	engine := compute.NewComputeEngine(logger, mockPeerClient, 1, "test-node")
	defer engine.Close()

	schema := protocol.ServiceSchema{
		Name: "mockBin",
		Parameters: map[string]protocol.ServiceParameter{
			"test_scenario": {Type: "string", Required: false},
			"original_data": {Type: "string", Required: false},
		},
	}

	mockBinPath := setupMockExecutable(t)
	handler := compute.BuildScriptHandler(mockBinPath)
	err := engine.RegisterNewService(schema, handler) 
	require.NoError(t, err)

	t.Run("Successful Execution", func(t *testing.T) {
		taskID := "job-should-success"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "mockBin",
			Payload: map[string]any{
				"test_scenario": "success",
				"original_data": "proxyma_test",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			// Assertions on the task response
			require.Equal(t, "completed", resp.Status)
			require.Equal(t, taskID, resp.TaskID)
			require.Equal(t, "mockBin", resp.Service)
			require.Equal(t, "completed", resp.Status)
			// Assertions on the mock results
			require.Equalf(t, "ok", resp.Outputs["status"], "Expected status 'ok', got: %v", resp.Outputs["status"])
			require.Equalf(t, "mock_binary", resp.Outputs["processed_by"], "Expected processed_by 'mock_binary', got: %v", resp.Outputs["processed_by"])
			require.Equalf(t, "proxyma_test", resp.Outputs["original_data"], "Expected original_data to be preserved, got: %v", resp.Outputs["original_data"])
			return true
		}, 2*time.Second, 100*time.Millisecond, "Worker failed to process the task in time")
	})

	t.Run("Script Execution Failure", func(t *testing.T) {
		taskID := "job-should-fail"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "mockBin",
			Payload: map[string]any{
				"test_scenario": "fail_execution",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		// Ensure the error surfaces the stderr from the script for debugging
		expectedSubStr := "simulated script crash"
		var resp protocol.ServiceTaskResponse
		require.Eventuallyf(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			require.Equal(t, taskID, resp.TaskID)
			require.Equal(t, "mockBin", resp.Service)
			require.Equal(t, "failed", resp.Status)
			require.Empty(t, resp.Outputs)
			return strings.Contains(resp.Error, expectedSubStr)
		}, 2*time.Second, 100*time.Millisecond, "Expected error to contain %q, got %q", expectedSubStr, resp.Error)
	})

	t.Run("Invalid JSON Response", func(t *testing.T) {
		taskID := "job-invalid-json"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "mockBin",
			Payload: map[string]any{
				"test_scenario": "invalid_json",
			},
		}
		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists{
				return false
			}
			require.Equal(t, taskID, resp.TaskID)
			require.Equal(t, "mockBin", resp.Service)
			require.Equal(t, "failed", resp.Status)
			require.Empty(t, resp.Outputs)
			require.NotEmpty(t, resp.Error)
			return true
		}, 2*time.Second, 100*time.Millisecond, "Expected an error due to invalid JSON output, but got nil")
	})
}

// setupMockGRPCWebhookServer creates an HTTP server that simulates a gRPC/Webhook endpoint.
// It parses incoming JSON requests and acts based on the "test_scenario" parameter.
func setupMockGRPCWebhookServer(t *testing.T) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate gRPC content type check or webhook validation
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		scenario, _ := payload["test_scenario"].(string)

		switch scenario {
		case "success":
			w.Header().Set("Content-Type", "application/json") // Or application/grpc+json
			w.WriteHeader(http.StatusOK)
			payload["status"] = "ok"
			payload["processed_by"] = "grpc_webhook_mock"
			json.NewEncoder(w).Encode(payload)

		case "fail_server_error":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal gRPC server error"}`))

		case "fail_timeout":
			// Simulate a hanging RPC call that exceeds the context timeout
			time.Sleep(3 * time.Second)
			w.WriteHeader(http.StatusOK)

		case "invalid_response":
			// Simulate a corrupted response from the remote node
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{ "broken_json": true, `))

		default:
			http.Error(w, "unknown scenario", http.StatusBadRequest)
		}
	})

	return httptest.NewServer(handler)
}

func TestWorkerExecutesTaskViaGRPCHandler(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mockPeerClient := &testutil.MockPeerClient{}
	
	engine := compute.NewComputeEngine(logger, mockPeerClient, 1, "test-node-grpc")
	defer engine.Close()

	mockServer := setupMockGRPCWebhookServer(t)
	defer mockServer.Close()

	schema := protocol.ServiceSchema{
		Name: "remoteGRPCService",
		Parameters: map[string]protocol.ServiceParameter{
			"test_scenario": {Type: "string", Required: true},
			"auth_token":    {Type: "string", Required: false},
		},
	}

	handler := compute.BuildGRPCHandler(mockServer.URL, 2*time.Second)
	err := engine.RegisterNewService(schema, handler)
	require.NoError(t, err)

	t.Run("Successful Webhook Execution", func(t *testing.T) {
		taskID := "grpc-job-success"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "remoteGRPCService",
			Payload: map[string]any{
				"test_scenario": "success",
				"auth_token":    "secret-123",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			require.Equal(t, "completed", resp.Status)
			require.Equal(t, taskID, resp.TaskID)
			require.Equal(t, "remoteGRPCService", resp.Service)
			require.Equal(t, "ok", resp.Outputs["status"])
			require.Equal(t, "grpc_webhook_mock", resp.Outputs["processed_by"])
			return true
		}, 2*time.Second, 100*time.Millisecond, "Expected successful remote gRPC execution")
	})

	t.Run("Remote Server Returns Error", func(t *testing.T) {
		taskID := "grpc-job-server-error"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "remoteGRPCService",
			Payload: map[string]any{
				"test_scenario": "fail_server_error",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			require.Equal(t, "failed", resp.Status)
			require.Contains(t, resp.Error, "500") // Assuming handler wraps the HTTP/gRPC status
			return true
		}, 2*time.Second, 100*time.Millisecond, "Expected task to fail gracefully on server error")
	})

	t.Run("Remote Webhook Timeout", func(t *testing.T) {
		taskID := "grpc-job-timeout"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "remoteGRPCService",
			Payload: map[string]any{
				"test_scenario": "fail_timeout",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			require.Equal(t, "failed", resp.Status)
			require.Contains(t, strings.ToLower(resp.Error), "context deadline exceeded") 
			return true
		}, 4*time.Second, 100*time.Millisecond, "Expected task to fail due to context timeout")
	})

	t.Run("Invalid Payload from Remote Webhook", func(t *testing.T) {
		taskID := "grpc-job-invalid-resp"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "remoteGRPCService",
			Payload: map[string]any{
				"test_scenario": "invalid_response",
			},
		}

		err = engine.SubmitTask(taskReq)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			resp, exists := engine.GetTaskResponse(taskID)
			if !exists {
				return false
			}
			require.Equal(t, "failed", resp.Status)
			require.NotEmpty(t, resp.Error)
			return true
		}, 2*time.Second, 100*time.Millisecond, "Expected task to fail due to unmarshallable response")
	})
}
