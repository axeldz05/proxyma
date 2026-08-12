package compute_test

import (
	"context"
	"os"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCannotRegisterDuplicateServices(t *testing.T) {
	registry := compute.NewServiceRegistry()

	schema := protocol.ServiceSchema{Name: "ocr"}

	var mockHandler compute.ServiceHandler = func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}
	err1 := registry.Register(schema, mockHandler)
	err2 := registry.Register(schema, mockHandler)

	require.NoError(t, err1)
	require.ErrorIs(t, err2, compute.ErrServiceDuplicate)
}

func TestWorkerExecutesTaskAndStoresResult(t *testing.T) {
	logger := protocol.NewLogger(os.Stdout, false)
	mockPeerClient := &testutil.MockPeerClient{}
	engine := compute.NewComputeEngine(context.Background(), logger, mockPeerClient, 1, "test-node")
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
			if !exists {
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

func TestWorkerExecutesTaskViaGRPCHandler(t *testing.T) {
	logger := protocol.NewLogger(os.Stdout, false)
	mockPeerClient := &testutil.MockPeerClient{}

	engine := compute.NewComputeEngine(context.Background(), logger, mockPeerClient, 1, "test-node-grpc")
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

	handler := compute.BuildHTTPHandler(mockServer.URL, 2*time.Second)
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

func TestPipelineStepInputValidation(t *testing.T) {
	logger := protocol.NewLogger(os.Stdout, false)
	mockPeerClient := &testutil.MockPeerClient{}
	engine := compute.NewComputeEngine(context.Background(), logger, mockPeerClient, 1, "test-node")
	defer engine.Close()

	// Register a service with required parameters
	schema := protocol.ServiceSchema{
		Name: "strictService",
		Parameters: map[string]protocol.ServiceParameter{
			"required_str": {Type: "string", Required: true},
			"required_int": {Type: "int", Required: true},
		},
	}

	handler := compute.ServiceHandler(func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok"}, nil
	})
	err := engine.RegisterNewService(schema, handler)
	require.NoError(t, err)

	t.Run("Fails validation when parameter is missing", func(t *testing.T) {
		taskID := "strict-job-fail-missing"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "strictService",
			Payload: map[string]any{
				"required_str": "hello",
				// missing required_int
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
			require.Contains(t, resp.Error, "missing required parameter: 'required_int'")
			return true
		}, 2*time.Second, 100*time.Millisecond)
	})

	t.Run("Fails validation when parameter type is invalid", func(t *testing.T) {
		taskID := "strict-job-fail-type"
		taskReq := protocol.TaskRequest{
			TaskID:  taskID,
			Service: "strictService",
			Payload: map[string]any{
				"required_str": "hello",
				"required_int": "not-an-int",
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
			require.Contains(t, resp.Error, "invalid type for parameter 'required_int'")
			return true
		}, 2*time.Second, 100*time.Millisecond)
	})
}
