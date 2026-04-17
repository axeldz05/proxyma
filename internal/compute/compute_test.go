package compute_test

import (
	"context"
	"log/slog"
	"os"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/mitchellh/mapstructure"
)

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
		Name: "math_add",
		Parameters: map[string]protocol.ServiceParameter{
			"a": {Type: "int", Required: true},
			"b": {Type: "int", Required: true},
		},
	}

	type AddArgs struct {
		A int `mapstructure:"a"`
		B int `mapstructure:"b"`
	}

	handler := func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		var args AddArgs
		if err := mapstructure.WeakDecode(payload, &args); err != nil {
			return nil, err
		}

		return map[string]any{"result": args.A + args.B}, nil
	}

	err := engine.RegisterNewService(schema, handler) 
	require.NoError(t, err)

	taskID := "job-123"
	taskReq := protocol.TaskRequest{
		TaskID:  "job-123",
		Service: "math_add",
		Payload: map[string]any{"a": 5, "b": 7},
	}

	err = engine.SubmitTask(taskReq)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, exists := engine.GetTaskStatus(taskID)
		if !exists {
			return false
		}
		require.Equal(t, "completed", status.Status)
		require.Equal(t, 12, status.Outputs["result"])
		return true
	}, 2*time.Second, 100*time.Millisecond, "Worker failed to process the task in time")
}
