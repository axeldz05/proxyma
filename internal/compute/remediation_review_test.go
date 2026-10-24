package compute

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
)

func escrowTestRequest(t *testing.T, cn string, req protocol.TaskEscrowRequest) *http.Request {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq := httptest.NewRequest(http.MethodPost, "/services/escrow", bytes.NewReader(data))
	httpReq.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}},
	}
	return httpReq
}

func TestEscrowSupervisor_NoWALWriteAmplificationWhenCandidateMissing(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	engine.SetTaskJournal(storageEngine)

	pipeSchema := protocol.PipelineSchema{
		ID:      "test-pipe-amp",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "local-service"},
			{ID: "step1", Service: "unresolvable-missing-service"},
		},
	}
	require.NoError(t, engine.RegisterPipeline(pipeSchema))

	// No candidates available for unresolvable-missing-service
	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		return "", "", protocol.ServiceSchema{}, errors.New("no peer provides this service")
	})

	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          "task-no-amp-1",
		Service:         pipeSchema.ID,
		RequesterNodeID: "coord-node",
		ReplyTo:         "https://coord-node.proxyma.local/callback",
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
	require.NoError(t, engine.PutTaskInEscrow(escrowReq))

	// Count journal records after initial PutTaskInEscrow
	initialJournal, err := storageEngine.GetTaskJournal("task-no-amp-1")
	require.NoError(t, err)
	initialCount := len(initialJournal)
	require.GreaterOrEqual(t, initialCount, 1)

	// Call ResumeEscrowTask multiple times (simulating supervisor ticks)
	for i := 0; i < 5; i++ {
		resumeErr := engine.ResumeEscrowTask("task-no-amp-1")
		require.Error(t, resumeErr)
		require.Contains(t, resumeErr.Error(), "no candidate node available")
	}

	// Also call WakeupEscrowTasks
	resumed := engine.WakeupEscrowTasks()
	require.Equal(t, 0, resumed)

	// Verify task is still in escrow in memory
	_, inEscrow := engine.GetEscrowTask("task-no-amp-1")
	require.True(t, inEscrow, "task must remain in escrow")

	// Verify journal records in WAL have NOT multiplied
	currentJournal, err := storageEngine.GetTaskJournal("task-no-amp-1")
	require.NoError(t, err)
	require.Equal(t, initialCount, len(currentJournal), "WAL checkpoint count must NOT increase when candidate is missing")
}

func TestHandleServiceEscrow_AuthenticationAndAuthorization(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-auth")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	task := protocol.TaskRequest{
		TaskID:                 "escrow-auth-task",
		Service:                "test-pipeline",
		ExpectedProducerNodeID: "worker-alice",
	}
	engine.RegisterOutgoingTask(task)

	// 1. Unknown task ID -> 404 Not Found
	rec := httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-alice", protocol.TaskEscrowRequest{
		TaskID:          "unknown-task-id",
		Service:         "test-pipeline",
		ProducerNodeID:  "worker-alice",
		RequesterNodeID: "coord-auth",
	}))
	require.Equal(t, http.StatusNotFound, rec.Code)

	// 2. Producer mismatch in body vs TLS CN -> 403 Forbidden
	rec = httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-alice", protocol.TaskEscrowRequest{
		TaskID:          task.TaskID,
		Service:         task.Service,
		ProducerNodeID:  "worker-bob",
		RequesterNodeID: "coord-auth",
	}))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// 3. Unauthorized producer (not expectedProducer and not delegated) -> 403 Forbidden
	rec = httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-mallory", protocol.TaskEscrowRequest{
		TaskID:          task.TaskID,
		Service:         task.Service,
		ProducerNodeID:  "worker-mallory",
		RequesterNodeID: "coord-auth",
	}))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// 4. Authorized producer -> 200 OK
	rec = httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-alice", protocol.TaskEscrowRequest{
		TaskID:          task.TaskID,
		Service:         task.Service,
		ProducerNodeID:  "worker-alice",
		RequesterNodeID: "coord-auth",
	}))
	require.Equal(t, http.StatusOK, rec.Code)

	// Verify placed in escrow
	_, inEscrow := engine.GetEscrowTask(task.TaskID)
	require.True(t, inEscrow)

	// 5. Already terminal task cannot be escrowed -> 409 Conflict
	terminalTask := protocol.TaskRequest{
		TaskID:                 "terminal-task-1",
		Service:                "test-pipeline",
		ExpectedProducerNodeID: "worker-alice",
	}
	engine.RegisterOutgoingTask(terminalTask)
	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  terminalTask.TaskID,
		Service: terminalTask.Service,
		Status:  "completed",
	})
	rec = httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-alice", protocol.TaskEscrowRequest{
		TaskID:          terminalTask.TaskID,
		Service:         terminalTask.Service,
		ProducerNodeID:  "worker-alice",
		RequesterNodeID: "coord-auth",
	}))
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestFailoverAndDynamicMigration_DelegatedProducersCallbackAuthorization(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-failover")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	taskID := "failover-auth-task"
	task := protocol.TaskRequest{
		TaskID:                 taskID,
		Service:                "pipeline-failover",
		ExpectedProducerNodeID: "initial-worker",
	}
	engine.RegisterOutgoingTask(task)

	// Initial worker is authorized, but alt-worker is not yet
	require.ErrorIs(t, engine.AcceptTaskCallback("alt-worker", protocol.ServiceTaskResponse{
		TaskID:  taskID,
		Service: "pipeline-failover",
		Status:  "completed",
	}), ErrTaskProducer)

	// Simulate failover step updating delegatedProducers (as dispatchStepWithFailover does)
	engine.taskStatusesMu.Lock()
	meta := engine.taskStatusMeta[taskID]
	if meta.delegatedProducers == nil {
		meta.delegatedProducers = make(map[string]struct{})
	}
	meta.delegatedProducers["alt-worker"] = struct{}{}
	engine.taskStatusMeta[taskID] = meta
	engine.taskStatusesMu.Unlock()

	// Now alt-worker is authorized
	require.NoError(t, engine.AcceptTaskCallback("alt-worker", protocol.ServiceTaskResponse{
		TaskID:  taskID,
		Service: "pipeline-failover",
		Status:  "completed",
	}))
}

func TestCoordinatorNodeID_URLHandling(t *testing.T) {
	t.Parallel()

	// 1. Peer local host (.proxyma.local)
	req1 := protocol.TaskRequest{
		ReplyTo: "https://node-1.proxyma.local:8443/services/callback",
	}
	require.Equal(t, "node-1", coordinatorNodeID(req1))

	// 2. Direct IPv4 URL
	req2 := protocol.TaskRequest{
		ReplyTo: "https://192.168.1.100:8443/services/callback",
	}
	require.Equal(t, "https://192.168.1.100:8443", coordinatorNodeID(req2))

	// 3. Direct IPv6 URL
	req3 := protocol.TaskRequest{
		ReplyTo: "https://[::1]:9443/services/callback",
	}
	require.Equal(t, "https://[::1]:9443", coordinatorNodeID(req3))

	// 4. RequesterNodeID takes precedence
	req4 := protocol.TaskRequest{
		RequesterNodeID: "explicit-node",
		ReplyTo:         "https://192.168.1.100:8443/services/callback",
	}
	require.Equal(t, "explicit-node", coordinatorNodeID(req4))

	// 5. OutputProducers $initial takes precedence over ReplyTo
	req5 := protocol.TaskRequest{
		PipelineState: &protocol.PipelineExecutionState{
			OutputProducers: map[string]string{"$initial": "initial-producer"},
		},
		ReplyTo: "https://192.168.1.100:8443/services/callback",
	}
	require.Equal(t, "initial-producer", coordinatorNodeID(req5))
}

func TestPreparePipelineTargets_EmbeddedSnapshotFallback(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "worker-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	snapshot := protocol.PipelineSchema{
		ID:      "remote-pipeline-snapshot",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step-0", Service: "remote-service", TargetNodeID: "target-peer-1"},
		},
	}

	taskReq := protocol.TaskRequest{
		TaskID:  "snapshot-task-1",
		Service: "remote-pipeline-snapshot", // not registered in engine.GetPipeline
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      "remote-pipeline-snapshot",
			PipelineVersion: 1,
			Snapshot:        &snapshot,
		},
	}

	require.NoError(t, engine.PreparePipelineTargets(&taskReq))
	require.NotNil(t, taskReq.PipelineState.SelectedTargets)
	require.Equal(t, "target-peer-1", taskReq.PipelineState.SelectedTargets["step-0"])
}

func TestAcceptedTasksCleanup_EscrowAndRemoteDispatch(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "local-coord")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	taskReq := protocol.TaskRequest{
		TaskID:          "accepted-task-cleanup-1",
		Service:         "dummy-service",
		RequesterNodeID: "local-coord",
	}
	engine.acceptedTasks.Store(taskReq.TaskID, taskReq)

	// Local escrow escalation
	err = engine.escalateToEscrow(taskReq, protocol.PipelineStep{ID: "step0", Service: "dummy-service"}, 0, errors.New("local fail"))
	require.NoError(t, err)

	// acceptedTasks must not retain the task
	_, exists := engine.acceptedTasks.Load(taskReq.TaskID)
	require.False(t, exists, "local escrow escalation must clean up acceptedTasks")
}

func TestAcceptTaskCallback_DelegatedProducerWithEmptyExpectedProducer(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	taskReq := protocol.TaskRequest{
		TaskID:  "task-delegated-only",
		Service: "pipe-delegated",
		// ExpectedProducerNodeID intentionally left empty
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      "pipe-delegated",
			PipelineVersion: 1,
			SelectedTargets: map[string]string{
				"step0": "worker-delegated",
			},
		},
	}
	engine.RegisterOutgoingTask(taskReq)

	// Callback from worker-delegated must succeed even though expectedProducer is empty
	err = engine.AcceptTaskCallback("worker-delegated", protocol.ServiceTaskResponse{
		TaskID:  "task-delegated-only",
		Service: "pipe-delegated",
		Status:  "completed",
	})
	require.NoError(t, err, "delegated producer must be accepted even when expectedProducer is empty")

	// Callback from random worker must be rejected
	err = engine.AcceptTaskCallback("worker-unauthorized", protocol.ServiceTaskResponse{
		TaskID:  "task-delegated-only",
		Service: "pipe-delegated",
		Status:  "completed",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTaskProducer)
}

func TestHandleServiceEscrow_RetiredTaskReturnsConflict(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	taskID := "retired-terminal-task"
	// Directly place in retiredTaskStatuses
	engine.taskStatusesMu.Lock()
	engine.retiredTaskStatuses[taskID] = protocol.ServiceTaskResponse{
		TaskID:  taskID,
		Service: "pipe-retired",
		Status:  "completed",
	}
	engine.taskStatusesMu.Unlock()

	rec := httptest.NewRecorder()
	engine.HandleServiceEscrow(rec, escrowTestRequest(t, "worker-1", protocol.TaskEscrowRequest{
		TaskID:          taskID,
		Service:         "pipe-retired",
		ProducerNodeID:  "worker-1",
		RequesterNodeID: "coord-node",
	}))
	require.Equal(t, http.StatusConflict, rec.Code, "retired completed task must return 409 Conflict on escrow")
}

func TestProcessTask_EmbeddedSnapshotFallbackWithoutLocalPipeline(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "worker-step1")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	// Register local handler for step1 service only (pipeline itself is NOT registered on this worker)
	var stepExecuted atomic.Bool
	require.NoError(t, engine.registry.Register(protocol.ServiceSchema{
		Name: "worker-calc-service",
	}, func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		stepExecuted.Store(true)
		return map[string]any{"calc": 100}, nil
	}))

	snapshot := protocol.PipelineSchema{
		ID:      "remote-pipeline-unknown-locally",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "some-step0-service"},
			{ID: "step1", Service: "worker-calc-service", TargetNodeID: "worker-step1"},
		},
	}

	taskReq := protocol.TaskRequest{
		TaskID:          "multi-step-remote-task",
		Service:         "remote-pipeline-unknown-locally",
		RequesterNodeID: "coord-node",
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      snapshot.ID,
			PipelineVersion: 1,
			CurrentStep:     1,
			Snapshot:        &snapshot,
			Outputs: map[string]map[string]any{
				"step0": {"data": "step0-result"},
			},
			OutputProducers: map[string]string{
				"$initial": "coord-node",
				"step0":    "remote-worker-0",
			},
			SelectedTargets: map[string]string{
				"step0": "remote-worker-0",
				"step1": "worker-step1",
			},
		},
	}

	// Submit task locally for step 1 execution
	require.NoError(t, engine.SubmitTask(taskReq))

	// Wait briefly for worker goroutine to process the step
	require.Eventually(t, func() bool {
		return stepExecuted.Load()
	}, 2*time.Second, 20*time.Millisecond, "step 1 must execute using embedded snapshot")
}

func TestPutTaskInEscrow_DuplicateRetransmitDoesNotWriteDuplicateWAL(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	storageEngine, err := storage.NewStorageEngine(logger, t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageEngine.Close() })

	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "coord-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	engine.SetTaskJournal(storageEngine)

	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          "retransmit-escrow-task",
		Service:         "pipe-test",
		RequesterNodeID: "coord-node",
		StepID:          "step1",
		StepIndex:       1,
		Status:          protocol.TaskStatusEscrow,
		Timestamp:       time.Now().UTC(),
	}

	// First escrow request
	require.NoError(t, engine.PutTaskInEscrow(escrowReq))
	j1, err := storageEngine.GetTaskJournal("retransmit-escrow-task")
	require.NoError(t, err)
	count1 := len(j1)
	require.GreaterOrEqual(t, count1, 1)

	// Duplicate retransmitted escrow request with same step index
	require.NoError(t, engine.PutTaskInEscrow(escrowReq))
	j2, err := storageEngine.GetTaskJournal("retransmit-escrow-task")
	require.NoError(t, err)
	require.Equal(t, count1, len(j2), "duplicate escrow at same step index must not append duplicate WAL record")
}

func TestRoutePipelineStep_PreselectedTargetTrackedInDelegatedProducers(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := NewComputeEngine(context.Background(), logger, nil, 1, "worker-node")
	require.NoError(t, err)
	t.Cleanup(engine.Close)

	var dispatchedTarget string
	engine.SetTaskDispatcher(func(targetPeerID string, req protocol.TaskRequest) error {
		dispatchedTarget = targetPeerID
		return nil
	})

	taskID := "preselected-target-task"
	step := protocol.PipelineStep{ID: "step0", Service: "remote-step-svc"}
	schema := protocol.PipelineSchema{
		ID:      "pipe-preselected",
		Version: 1,
		Steps:   []protocol.PipelineStep{step},
	}

	taskReq := protocol.TaskRequest{
		TaskID:  taskID,
		Service: "pipe-preselected",
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      "pipe-preselected",
			PipelineVersion: 1,
			SelectedTargets: map[string]string{
				"step0": "preselected-peer-99",
			},
		},
	}
	engine.RegisterOutgoingTask(taskReq)

	// Route step where target is already in SelectedTargets
	engine.routePipelineStep(taskReq, step, schema)
	require.Equal(t, "preselected-peer-99", dispatchedTarget)

	// Verify that preselected-peer-99 was recorded into delegatedProducers
	engine.taskStatusesMu.Lock()
	meta := engine.taskStatusMeta[taskID]
	_, delegated := meta.delegatedProducers["preselected-peer-99"]
	engine.taskStatusesMu.Unlock()
	require.True(t, delegated, "preselected targetPeer must be tracked in delegatedProducers")

	// Verify callback from preselected-peer-99 is authorized
	require.NoError(t, engine.AcceptTaskCallback("preselected-peer-99", protocol.ServiceTaskResponse{
		TaskID:  taskID,
		Service: "pipe-preselected",
		Status:  "completed",
	}))
}


