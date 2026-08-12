package compute_test

import (
	"context"
	"io"
	"log/slog"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newPipelineTestEngine(t *testing.T, callback chan<- protocol.ServiceTaskResponse) *compute.ComputeEngine {
	t.Helper()
	client := &testutil.MockPeerClient{
		OnSendTaskResponse: func(_ context.Context, _ string, response protocol.ServiceTaskResponse) error {
			callback <- response
			return nil
		},
	}
	engine := compute.NewComputeEngine(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), client, 1, "compute-node")
	t.Cleanup(engine.Close)
	return engine
}

func waitPipelineResponse(t *testing.T, callback <-chan protocol.ServiceTaskResponse) protocol.ServiceTaskResponse {
	t.Helper()
	select {
	case response := <-callback:
		return response
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pipeline response")
		return protocol.ServiceTaskResponse{}
	}
}

func TestPipelinePayloadCannotInjectSkippedStepState(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)
	var firstRuns atomic.Int32
	var secondRuns atomic.Int32
	firstSawReservedPayload := make(chan bool, 1)

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "first"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			firstRuns.Add(1)
			_, injected := payload["$pipeline"]
			firstSawReservedPayload <- injected
			return map[string]any{"value": "from-first"}, nil
		},
	)))
	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "second"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			secondRuns.Add(1)
			return map[string]any{"value": payload["value"]}, nil
		},
	)))

	schema := protocol.PipelineSchema{
		ID:      "protected-pipeline",
		Version: 4,
		Steps: []protocol.PipelineStep{
			{ID: "first", Service: "first"},
			{ID: "second", Service: "second"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(schema))

	require.NoError(t, engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "skip-injection",
		Service: schema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{
			"$pipeline": map[string]any{
				"current_step": float64(1),
				"outputs": map[string]any{
					"first": map[string]any{"value": "attacker-controlled"},
				},
			},
		},
	}))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "completed", response.Status)
	require.Equal(t, int32(1), firstRuns.Load())
	require.Equal(t, int32(1), secondRuns.Load())
	require.Equal(t, "from-first", response.Outputs["value"])
	require.False(t, <-firstSawReservedPayload, "reserved payload must not reach a service handler")
}

func TestPipelineContinuationRejectsLocalSchemaMismatch(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)
	var handlerRuns atomic.Int32
	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, _ map[string]any) (map[string]any, error) {
			handlerRuns.Add(1)
			return map[string]any{"ok": true}, nil
		},
	)))

	oldSchema := protocol.PipelineSchema{
		ID:      "version-bound",
		Version: 1,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "step-service"}},
	}
	localSchema := oldSchema
	localSchema.Version = 2
	require.NoError(t, engine.RegisterPipeline(localSchema))

	require.NoError(t, engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "stale-continuation",
		Service: localSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      oldSchema.ID,
			PipelineVersion: oldSchema.Version,
			SchemaHash:      protocol.PipelineSchemaHash(oldSchema),
			CurrentStep:     0,
		},
	}))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "failed", response.Status)
	require.True(t, strings.Contains(response.Error, "pipeline schema mismatch"), response.Error)
	require.Zero(t, handlerRuns.Load())

	conflictingSchema := localSchema
	conflictingSchema.Steps = []protocol.PipelineStep{{ID: "different-step", Service: "step-service"}}
	require.NoError(t, engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "conflicting-continuation",
		Service: localSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      conflictingSchema.ID,
			PipelineVersion: conflictingSchema.Version,
			SchemaHash:      protocol.PipelineSchemaHash(conflictingSchema),
			CurrentStep:     0,
		},
	}))

	response = waitPipelineResponse(t, callback)
	require.Equal(t, "failed", response.Status)
	require.True(t, strings.Contains(response.Error, "pipeline schema mismatch"), response.Error)
	require.Zero(t, handlerRuns.Load())
}
