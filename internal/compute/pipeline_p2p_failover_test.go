package compute_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"
)

func newPipelineTestEngineWithStorage(t *testing.T, callback chan<- protocol.ServiceTaskResponse, nodeID string) (*compute.ComputeEngine, *storage.StorageEngine) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	client := &testutil.MockPeerClient{
		OnSendTaskResponse: func(_ context.Context, _ string, response protocol.ServiceTaskResponse) error {
			callback <- response
			return nil
		},
	}
	engine, err := compute.NewComputeEngine(context.Background(), logger, client, 1, nodeID)
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	engine.SetTaskJournal(storageEngine)
	return engine, storageEngine
}

func TestPipelineInFlightSchemaMigration_ValidPrefix(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	var step0Runs atomic.Int32
	var step1Runs atomic.Int32
	var step2Runs atomic.Int32

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step0-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step0Runs.Add(1)
			return map[string]any{"msg": "from-step0"}, nil
		},
	)))

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step1-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step1Runs.Add(1)
			return map[string]any{"msg": "from-step1", "combined": payload["msg"].(string) + "+step1"}, nil
		},
	)))

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step2-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step2Runs.Add(1)
			return map[string]any{"final": payload["combined"].(string) + "+step2"}, nil
		},
	)))

	// Setup storage engine to verify WAL TaskEventSchemaMigrated
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })
	engine.SetTaskJournal(storageEngine)

	v1Schema := protocol.PipelineSchema{
		ID:      "migrate-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-service"},
			{ID: "step1", Service: "step1-service"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "msg", ToStep: "step1", ToPort: "msg"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(v1Schema))

	// Prepare task snapshot bound to v1
	taskReq := protocol.TaskRequest{
		TaskID:  "task-migrate-1",
		Service: v1Schema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.BindPipelineTask(&taskReq))

	// Now register v2 in the engine adding step2 before step1 executes
	v2Schema := protocol.PipelineSchema{
		ID:      "migrate-pipeline",
		Version: 2,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-service"},
			{ID: "step1", Service: "step1-service"},
			{ID: "step2", Service: "step2-service"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "msg", ToStep: "step1", ToPort: "msg"},
			{FromStep: "step1", FromPort: "combined", ToStep: "step2", ToPort: "combined"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(v2Schema))

	// Submit task
	require.NoError(t, engine.SubmitTask(taskReq))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "completed", response.Status, "response error: %s", response.Error)
	require.Equal(t, int32(1), step0Runs.Load())
	require.Equal(t, int32(1), step1Runs.Load())
	require.Equal(t, int32(1), step2Runs.Load(), "step2 from v2 schema must have executed after migration")
	require.Equal(t, "from-step0+step1+step2", response.Outputs["final"])

	// Verify WAL recorded TaskEventSchemaMigrated
	journal, err := storageEngine.GetTaskJournal("task-migrate-1")
	require.NoError(t, err)
	var foundMigratedEvent bool
	for _, rec := range journal {
		if rec.Status == protocol.TaskEventSchemaMigrated {
			foundMigratedEvent = true
			require.Equal(t, "1", rec.Metadata["from_version"])
			require.Equal(t, "2", rec.Metadata["to_version"])
		}
	}
	require.True(t, foundMigratedEvent, "TaskEventSchemaMigrated must be recorded in WAL")
}

func TestPipelineInFlightSchemaMigration_InvalidPrefix_FallbackToSnapshot(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	var step0Runs atomic.Int32
	var step1Runs atomic.Int32

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step0-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step0Runs.Add(1)
			return map[string]any{"data": "original-step0"}, nil
		},
	)))

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step1-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step1Runs.Add(1)
			return map[string]any{"result": payload["data"].(string) + "+finished"}, nil
		},
	)))

	v1Schema := protocol.PipelineSchema{
		ID:      "fallback-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-service"},
			{ID: "step1", Service: "step1-service"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "data", ToStep: "step1", ToPort: "data"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(v1Schema))

	// Bind task to v1
	taskReq := protocol.TaskRequest{
		TaskID:  "task-fallback-1",
		Service: v1Schema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.BindPipelineTask(&taskReq))

	// Now update local engine to v2 with an invalid prefix (completed step0 is removed)
	v2InvalidSchema := protocol.PipelineSchema{
		ID:      "fallback-pipeline",
		Version: 2,
		Steps: []protocol.PipelineStep{
			{ID: "step_unrelated", Service: "step0-service"},
			{ID: "step1", Service: "step1-service"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(v2InvalidSchema))

	// Simulate that step0 has already completed in incoming state
	taskReq.PipelineState.CurrentStep = 1
	taskReq.PipelineState.Outputs["step0"] = map[string]any{"data": "precomputed-step0"}
	taskReq.PipelineState.OutputProducers["step0"] = "compute-node"

	// Submit task: dynamic migration should be rejected due to prefix invalidity,
	// and task must gracefully fall back to executing its starting snapshot!
	require.NoError(t, engine.SubmitTask(taskReq))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "completed", response.Status, "response error: %s", response.Error)
	require.Equal(t, int32(0), step0Runs.Load(), "step0 was already completed in snapshot state")
	require.Equal(t, int32(1), step1Runs.Load(), "step1 must have executed using snapshot")
	require.Equal(t, "precomputed-step0+finished", response.Outputs["result"])
}

func TestPipelineIntermediateNodeFailover_AlternativePeerFound(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	var dispatchedToOffline atomic.Int32
	var dispatchedToBackup atomic.Int32

	// taskDispatcher fails for offline-node, succeeds for backup-node
	engine.SetTaskDispatcher(func(targetPeerID string, req protocol.TaskRequest) error {
		if targetPeerID == "offline-node" {
			dispatchedToOffline.Add(1)
			return errors.New("connection refused: peer is offline")
		}
		if targetPeerID == "backup-node" {
			dispatchedToBackup.Add(1)
			// Simulate backup peer finishing step
			return nil
		}
		return errors.New("unexpected peer " + targetPeerID)
	})

	// ServiceFinder returns backup-node when queried
	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		if query.Service == "remote-step" {
			return "backup-node", "https://backup.invalid:8080", protocol.ServiceSchema{Name: "remote-step"}, nil
		}
		return "", "", protocol.ServiceSchema{}, errors.New("no service found")
	})

	pipeSchema := protocol.PipelineSchema{
		ID:      "failover-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "remote-step", TargetNodeID: "offline-node"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(pipeSchema))

	taskReq := protocol.TaskRequest{
		TaskID:  "task-failover-1",
		Service: pipeSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.BindPipelineTask(&taskReq))

	// Submit task: offline-node fails -> fails over to backup-node via serviceFinder
	require.NoError(t, engine.SubmitTask(taskReq))

	// Give async worker time to execute dispatch with failover
	require.Eventually(t, func() bool {
		return dispatchedToOffline.Load() >= 1 && dispatchedToBackup.Load() == 1
	}, 2*time.Second, 20*time.Millisecond)

	require.Equal(t, int32(1), dispatchedToBackup.Load(), "step must be dispatched to alternative peer backup-node")
}

func TestPipelineIntermediateNodeFailover_FallbackToLocal(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	var localStepRuns atomic.Int32
	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "resilient-service"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			localStepRuns.Add(1)
			return map[string]any{"recovered": true}, nil
		},
	)))

	// taskDispatcher fails because target peer is unreachable
	engine.SetTaskDispatcher(func(targetPeerID string, req protocol.TaskRequest) error {
		return errors.New("peer " + targetPeerID + " unreachable")
	})

	// ServiceFinder returns error (no alternative remote nodes available)
	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		return "", "", protocol.ServiceSchema{}, errors.New("no remote nodes available")
	})

	pipeSchema := protocol.PipelineSchema{
		ID:      "local-failover-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "resilient-service", TargetNodeID: "remote-unreachable"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(pipeSchema))

	taskReq := protocol.TaskRequest{
		TaskID:  "task-local-failover-1",
		Service: pipeSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.BindPipelineTask(&taskReq))

	// Submit task: remote unreachable -> serviceFinder fails -> fails over to local handler!
	require.NoError(t, engine.SubmitTask(taskReq))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "completed", response.Status, "response error: %s", response.Error)
	require.Equal(t, int32(1), localStepRuns.Load(), "local node must have executed the step as failover")
	require.Equal(t, true, response.Outputs["recovered"])
}

func TestPipelineWAL_IntermediateOutputCheckpointing(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })
	engine.SetTaskJournal(storageEngine)

	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step0"}, compute.BuildUnaryHandler(
		func(_ context.Context, _ map[string]any) (map[string]any, error) {
			return map[string]any{"step0_result": "inter-data"}, nil
		},
	)))
	require.NoError(t, engine.RegisterNewService(protocol.ServiceSchema{Name: "step1"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"final_result": payload["step0_result"].(string) + "+done"}, nil
		},
	)))

	pipeSchema := protocol.PipelineSchema{
		ID:      "wal-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0"},
			{ID: "step1", Service: "step1"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "step0_result", ToStep: "step1", ToPort: "step0_result"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(pipeSchema))

	taskReq := protocol.TaskRequest{
		TaskID:  "task-wal-1",
		Service: pipeSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.SubmitTask(taskReq))

	response := waitPipelineResponse(t, callback)
	require.Equal(t, "completed", response.Status)
	require.Equal(t, "inter-data+done", response.Outputs["final_result"])

	// Verify WAL records
	journal, err := storageEngine.GetTaskJournal("task-wal-1")
	require.NoError(t, err)
	require.NotEmpty(t, journal)

	var hasIntermediateStep0 bool
	var hasCompletedFinal bool

	for _, rec := range journal {
		if rec.StepID == "step0" && rec.Intermediate && rec.Status == "running" {
			hasIntermediateStep0 = true
			require.Equal(t, "inter-data", rec.Outputs["step0_result"])
		}
		if rec.Status == "completed" && !rec.Intermediate {
			hasCompletedFinal = true
			require.Equal(t, "inter-data+done", rec.Outputs["final_result"])
		}
	}

	require.True(t, hasIntermediateStep0, "WAL must have intermediate checkpoint for step0")
	require.True(t, hasCompletedFinal, "WAL must have completed checkpoint for pipeline")

	// Verify TaskRecord in history
	hist, exists, err := storageEngine.GetTaskHistory("task-wal-1")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "inter-data+done", hist.FinalOutputs["final_result"])
}

func TestPipelineIntermediateNodeFailover_MultipleCandidatesExcludedPeers(t *testing.T) {
	t.Parallel()

	callback := make(chan protocol.ServiceTaskResponse, 1)
	engine := newPipelineTestEngine(t, callback)

	var attemptedPeers []string
	var mu sync.Mutex

	engine.SetTaskDispatcher(func(targetPeerID string, req protocol.TaskRequest) error {
		mu.Lock()
		attemptedPeers = append(attemptedPeers, targetPeerID)
		mu.Unlock()

		if targetPeerID == "node-1" {
			return errors.New("node-1 connection refused")
		}
		if targetPeerID == "node-2" {
			return errors.New("node-2 timeout")
		}
		if targetPeerID == "node-3" {
			return nil
		}
		return errors.New("unexpected peer " + targetPeerID)
	})

	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		excluded := make(map[string]bool)
		protocol.PopulateMap(excluded, query.ExcludedPeers, true)

		candidates := []string{"node-1", "node-2", "node-3"}
		for _, c := range candidates {
			if !excluded[c] {
				return c, "https://" + c + ".invalid:8080", protocol.ServiceSchema{Name: "multi-svc"}, nil
			}
		}
		return "", "", protocol.ServiceSchema{}, errors.New("all candidates excluded")
	})

	pipeSchema := protocol.PipelineSchema{
		ID:      "multi-failover-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "multi-svc", TargetNodeID: "node-1"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(pipeSchema))

	taskReq := protocol.TaskRequest{
		TaskID:  "task-multi-failover-1",
		Service: pipeSchema.ID,
		ReplyTo: "https://requester.invalid/callback",
		Payload: map[string]any{},
	}
	require.NoError(t, engine.BindPipelineTask(&taskReq))

	require.NoError(t, engine.SubmitTask(taskReq))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(attemptedPeers) == 3 && attemptedPeers[2] == "node-3"
	}, 2*time.Second, 20*time.Millisecond)

	mu.Lock()
	require.Equal(t, []string{"node-1", "node-2", "node-3"}, attemptedPeers)
	mu.Unlock()
}

func TestPipelineCoordinatorEscrow_WorkerEscalatesOnCandidateExhaustion(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	coordCallback := make(chan protocol.ServiceTaskResponse, 1)
	coordEngine, err := compute.NewComputeEngine(context.Background(), logger, &testutil.MockPeerClient{
		OnSendTaskResponse: func(_ context.Context, _ string, resp protocol.ServiceTaskResponse) error {
			coordCallback <- resp
			return nil
		},
	}, 1, "node-coord")
	require.NoError(t, err)
	t.Cleanup(coordEngine.Close)
	coordEngine.SetTaskJournal(storageEngine)

	workerCallback := make(chan protocol.ServiceTaskResponse, 1)
	workerEngine, err := compute.NewComputeEngine(context.Background(), logger, &testutil.MockPeerClient{
		OnSendTaskResponse: func(_ context.Context, _ string, resp protocol.ServiceTaskResponse) error {
			workerCallback <- resp
			return nil
		},
	}, 1, "node-worker")
	require.NoError(t, err)
	t.Cleanup(workerEngine.Close)

	var escrowDelivered atomic.Bool
	workerEngine.SetTaskEscrowDispatcher(func(coordinatorPeerID string, req protocol.TaskEscrowRequest) error {
		require.Equal(t, "node-coord", coordinatorPeerID)
		require.Equal(t, "step1", req.StepID)
		require.Equal(t, 1, req.StepIndex)
		require.Equal(t, protocol.TaskStatusEscrow, req.Status)
		require.Equal(t, "node-worker", req.ProducerNodeID)
		require.Contains(t, req.Outputs, "step0")
		err := coordEngine.PutTaskInEscrow(req)
		escrowDelivered.Store(true)
		return err
	})

	workerEngine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		return "", "", protocol.ServiceSchema{}, errors.New("no provider for " + query.Service)
	})

	pipeSchema := protocol.PipelineSchema{
		ID:      "escrow-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-svc"},
			{ID: "step1", Service: "missing-step1-svc"},
		},
	}
	require.NoError(t, coordEngine.RegisterPipeline(pipeSchema))
	require.NoError(t, workerEngine.RegisterPipeline(pipeSchema))

	require.NoError(t, workerEngine.RegisterNewService(protocol.ServiceSchema{Name: "step0-svc"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"step0_out": "val0"}, nil
		},
	)))

	taskReq := protocol.TaskRequest{
		TaskID:          "task-escrow-escalate-1",
		Service:         pipeSchema.ID,
		RequesterNodeID: "node-coord",
		ReplyTo:         "https://coord.invalid/callback",
		Payload:         map[string]any{},
	}
	require.NoError(t, workerEngine.BindPipelineTask(&taskReq))

	// Submit task on worker
	require.NoError(t, workerEngine.SubmitTask(taskReq))

	require.Eventually(t, func() bool {
		return escrowDelivered.Load()
	}, 2*time.Second, 20*time.Millisecond)

	// Coordinator should have task in escrow
	escrowReq, inEscrow := coordEngine.GetEscrowTask(taskReq.TaskID)
	require.True(t, inEscrow)
	require.Equal(t, "step1", escrowReq.StepID)
	require.Equal(t, 1, escrowReq.StepIndex)

	// Task should be in bucketTasksActive in bbolt with status escrow
	activeTask, activeExists, err := storageEngine.GetActiveTask(taskReq.TaskID)
	require.NoError(t, err)
	require.True(t, activeExists)
	require.Equal(t, protocol.TaskStatusEscrow, activeTask.Status)
	require.Equal(t, 1, activeTask.CurrentStep)
	require.Equal(t, "step1", activeTask.CurrentStepID)
	require.Equal(t, "val0", activeTask.Outputs["step0"]["step0_out"])
}

func TestPipelineCoordinatorEscrow_MeshAnnouncementWakeupAndCompletion(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	coordCallback := make(chan protocol.ServiceTaskResponse, 1)
	coordEngine, err := compute.NewComputeEngine(context.Background(), logger, &testutil.MockPeerClient{
		OnSendTaskResponse: func(_ context.Context, _ string, resp protocol.ServiceTaskResponse) error {
			coordCallback <- resp
			return nil
		},
	}, 1, "node-coord")
	require.NoError(t, err)
	t.Cleanup(coordEngine.Close)
	coordEngine.SetTaskJournal(storageEngine)

	pipeSchema := protocol.PipelineSchema{
		ID:      "wakeup-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-svc"},
			{ID: "step1", Service: "late-arriving-svc"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "step0_out", ToStep: "step1", ToPort: "in_val"},
		},
	}
	require.NoError(t, coordEngine.RegisterPipeline(pipeSchema))

	// Put task directly in escrow (simulating escalation received earlier)
	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          "task-wakeup-1",
		Service:         pipeSchema.ID,
		RequesterNodeID: "node-coord",
		ReplyTo:         "https://coord.invalid/callback",
		StepID:          "step1",
		StepIndex:       1,
		Status:          protocol.TaskStatusEscrow,
		Outputs: map[string]map[string]any{
			"step0": {"step0_out": "initial_data"},
		},
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      pipeSchema.ID,
			PipelineVersion: 1,
			SchemaHash:      protocol.PipelineSchemaHash(pipeSchema),
			CurrentStep:     1,
			Outputs: map[string]map[string]any{
				"step0": {"step0_out": "initial_data"},
			},
			OutputProducers: map[string]string{
				"$initial": "node-coord",
				"step0":    "node-worker",
			},
		},
		Timestamp: time.Now().UTC(),
	}
	require.NoError(t, coordEngine.PutTaskInEscrow(escrowReq))

	// Verify it is currently in escrow
	_, inEscrow := coordEngine.GetEscrowTask("task-wakeup-1")
	require.True(t, inEscrow)

	// Now register the previously missing service on coordinator (or discovery candidate)
	var step1Executed atomic.Bool
	require.NoError(t, coordEngine.RegisterNewService(protocol.ServiceSchema{Name: "late-arriving-svc"}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			step1Executed.Store(true)
			return map[string]any{"final_result": payload["in_val"].(string) + "+completed"}, nil
		},
	)))

	// Trigger wakeup (like when peer announcement or new service is registered)
	resumed := coordEngine.WakeupEscrowTasks()
	require.Equal(t, 1, resumed)

	// Task should no longer be in escrow
	_, inEscrow = coordEngine.GetEscrowTask("task-wakeup-1")
	require.False(t, inEscrow)

	// Wait for pipeline completion
	resp := waitPipelineResponse(t, coordCallback)
	require.Equal(t, "completed", resp.Status, "resp error: %s", resp.Error)
	require.Equal(t, "initial_data+completed", resp.Outputs["final_result"])
	require.True(t, step1Executed.Load())

	// Task should now be in history
	hist, exists, err := storageEngine.GetTaskHistory("task-wakeup-1")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
}

func TestPipelineIntermediatePruning_PreservesFinalOutputAndCleansIntermediate(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	// Stage an intermediate blob
	interHash, _, err := storageEngine.SavePhysicalBlob(bytes.NewReader([]byte("intermediate transient payload")))
	require.NoError(t, err)
	hasInter, err := storageEngine.HasPhysicalBlob(interHash)
	require.NoError(t, err)
	require.True(t, hasInter)

	// Stage a final blob
	finalHash, _, err := storageEngine.SavePhysicalBlob(bytes.NewReader([]byte("final permanent pipeline artifact")))
	require.NoError(t, err)
	hasFinal, err := storageEngine.HasPhysicalBlob(finalHash)
	require.NoError(t, err)
	require.True(t, hasFinal)

	taskID := "task-prune-test-1"

	// 1. Append intermediate checkpoint
	err = storageEngine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:  taskID,
			Service: "pipe-prune",
			Status:  "running",
		},
		StepID:            "step0",
		StepIndex:         0,
		Intermediate:      true,
		IntermediateBlobs: []string{interHash},
		Outputs:           map[string]any{"temp": "vfs://" + interHash},
	})
	require.NoError(t, err)

	// 2. Append completed terminal checkpoint with final output
	err = storageEngine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:  taskID,
			Service: "pipe-prune",
			Status:  "completed",
		},
		StepID:       "step1",
		StepIndex:    1,
		Intermediate: false,
		Outputs:      map[string]any{"final": "vfs://" + finalHash},
	})
	require.NoError(t, err)

	// Verify journal before pruning contains both checkpoints
	journalBefore, err := storageEngine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journalBefore, 2)

	// 3. Execute pruning on completed task
	err = storageEngine.PruneIntermediateTaskData(taskID)
	require.NoError(t, err)

	// 4. Verify intermediate checkpoint was purged, terminal completed retained
	journalAfter, err := storageEngine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journalAfter, 1)
	require.Equal(t, "completed", journalAfter[0].Status)
	require.False(t, journalAfter[0].Intermediate)

	// 5. Verify intermediate blob was purged from physical storage
	hasInterAfter, err := storageEngine.HasPhysicalBlob(interHash)
	require.NoError(t, err)
	require.False(t, hasInterAfter, "intermediate blob must be pruned")

	// 6. Verify final blob remains intact and readable
	hasFinalAfter, err := storageEngine.HasPhysicalBlob(finalHash)
	require.NoError(t, err)
	require.True(t, hasFinalAfter, "final blob must be permanently preserved")

	// 7. Verify TaskRecord history is permanently retained
	hist, exists, err := storageEngine.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "vfs://"+finalHash, hist.FinalOutputs["final"])
}

func TestPipelineCoordinatorEscrow_PreservesInitialPayload(t *testing.T) {
	t.Parallel()

	coordCallback := make(chan protocol.ServiceTaskResponse, 1)
	coordEngine, storageEngine := newPipelineTestEngineWithStorage(t, coordCallback, "node-coord-payload")

	pipeSchema := protocol.PipelineSchema{
		ID:      "payload-escrow-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "step0-service"},
			{ID: "step1", Service: "step1-late-service"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "$initial", FromPort: "config_key", ToStep: "step1", ToPort: "cfg"},
			{FromStep: "step0", FromPort: "data", ToStep: "step1", ToPort: "in_data"},
		},
	}
	require.NoError(t, coordEngine.RegisterPipeline(pipeSchema))

	// Escrow request includes initial payload
	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          "task-payload-1",
		Service:         pipeSchema.ID,
		RequesterNodeID: "node-coord-payload",
		ReplyTo:         "https://coord.invalid/callback",
		StepID:          "step1",
		StepIndex:       1,
		Status:          protocol.TaskStatusEscrow,
		Payload: map[string]any{
			"config_key": "special-config-value",
		},
		Outputs: map[string]map[string]any{
			"step0": {"data": "step0_value"},
		},
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      pipeSchema.ID,
			PipelineVersion: 1,
			SchemaHash:      protocol.PipelineSchemaHash(pipeSchema),
			CurrentStep:     1,
			Outputs: map[string]map[string]any{
				"step0": {"data": "step0_value"},
			},
			OutputProducers: map[string]string{
				"$initial": "node-coord-payload",
				"step0":    "node-worker",
			},
		},
		Timestamp: time.Now().UTC(),
	}
	require.NoError(t, coordEngine.PutTaskInEscrow(escrowReq))

	// Register step1 which requires the $initial config_key
	var receivedConfig string
	require.NoError(t, coordEngine.RegisterNewService(protocol.ServiceSchema{
		Name: "step1-late-service",
		Parameters: map[string]protocol.ServiceParameter{
			"cfg":     {Type: protocol.ParamTypeString},
			"in_data": {Type: protocol.ParamTypeString},
		},
	}, compute.BuildUnaryHandler(
		func(_ context.Context, payload map[string]any) (map[string]any, error) {
			if cfg, ok := payload["cfg"].(string); ok {
				receivedConfig = cfg
			}
			return map[string]any{
				"combined": fmt.Sprintf("%v+%v", payload["in_data"], payload["cfg"]),
			}, nil
		},
	)))

	resumed := coordEngine.WakeupEscrowTasks()
	require.Equal(t, 1, resumed)

	resp := waitPipelineResponse(t, coordCallback)
	require.Equal(t, "completed", resp.Status)
	require.Equal(t, "special-config-value", receivedConfig, "initial payload parameter must be preserved through escrow")
	require.Equal(t, "step0_value+special-config-value", resp.Outputs["combined"])

	_ = storageEngine
}

func TestPipelineCoordinatorEscrow_DispatchFailureRevertsToEscrow(t *testing.T) {
	t.Parallel()

	coordCallback := make(chan protocol.ServiceTaskResponse, 1)
	coordEngine, storageEngine := newPipelineTestEngineWithStorage(t, coordCallback, "node-coord-revert")

	pipeSchema := protocol.PipelineSchema{
		ID:      "revert-escrow-pipe",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "s0"},
			{ID: "step1", Service: "s1-flaky"},
		},
	}
	require.NoError(t, coordEngine.RegisterPipeline(pipeSchema))

	// Discovery finds flaky-node for s1-flaky
	coordEngine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		if query.Service == "s1-flaky" {
			return "flaky-node", "https://flaky.invalid", protocol.ServiceSchema{Name: "s1-flaky"}, nil
		}
		return "", "", protocol.ServiceSchema{}, errors.New("not found")
	})

	// Dispatcher fails when sending to flaky-node
	coordEngine.SetTaskDispatcher(func(targetPeerID string, req protocol.TaskRequest) error {
		return errors.New("network timeout reaching flaky-node")
	})

	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          "task-revert-1",
		Service:         pipeSchema.ID,
		RequesterNodeID: "node-coord-revert",
		ReplyTo:         "https://coord.invalid/callback",
		StepID:          "step1",
		StepIndex:       1,
		Status:          protocol.TaskStatusEscrow,
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      pipeSchema.ID,
			PipelineVersion: 1,
			CurrentStep:     1,
		},
		Timestamp: time.Now().UTC(),
	}
	require.NoError(t, coordEngine.PutTaskInEscrow(escrowReq))

	// Resume should attempt dispatch to flaky-node, fail, and revert to escrow
	err := coordEngine.ResumeEscrowTask("task-revert-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "flaky-node")

	// Task must STILL be in escrow in memory
	_, inEscrow := coordEngine.GetEscrowTask("task-revert-1")
	require.True(t, inEscrow, "task must remain in escrow when dispatch fails")

	// Task status must NOT be left as 'running'
	taskResp, exists := coordEngine.GetTaskResponse("task-revert-1")
	require.True(t, exists)
	require.Equal(t, protocol.TaskStatusEscrow, taskResp.Status, "task status must revert to escrow, not running")

	// Task in storage active bucket must also have status 'escrow'
	activeTask, exists, err := storageEngine.GetActiveTask("task-revert-1")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, protocol.TaskStatusEscrow, activeTask.Status, "active record must revert to escrow in bbolt")

	// Late callback from failed dispatch target must be rejected
	err = coordEngine.AcceptTaskCallback("flaky-node", protocol.ServiceTaskResponse{
		TaskID:  "task-revert-1",
		Service: pipeSchema.ID,
		Status:  "completed",
	})
	require.ErrorIs(t, err, compute.ErrTaskProducer, "failed dispatch target must not be accepted as callback producer after revert")
}


