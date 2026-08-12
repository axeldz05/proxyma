package compute

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/protocol"
)

func newReviewCompute(t *testing.T, workers int) *ComputeEngine {
	t.Helper()
	engine := NewComputeEngine(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, workers, "requester")
	t.Cleanup(engine.Close)
	return engine
}

func callbackRequest(t *testing.T, cn string, response protocol.ServiceTaskResponse) *http.Request {
	t.Helper()
	body := bytes.NewReader(mustJSON(t, response))
	request := httptest.NewRequest(http.MethodPost, "/services/callback", body)
	state := tlsStateForCN(cn)
	request.TLS = &state
	return request
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return data
}

func tlsStateForCN(cn string) tls.ConnectionState {
	return tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}},
	}
}

func TestServiceCallbackAuthenticatesProducerAndKnownTask(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	task := protocol.TaskRequest{
		TaskID:                 "known",
		Service:                "pipeline",
		ExpectedProducerNodeID: "worker",
	}
	engine.RegisterOutgoingTask(task)

	wrongPeer := httptest.NewRecorder()
	engine.HandleServiceCallback(wrongPeer, callbackRequest(t, "intruder", protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
	}))
	if wrongPeer.Code != http.StatusForbidden {
		t.Fatalf("unexpected authenticated producer status = %d, want %d", wrongPeer.Code, http.StatusForbidden)
	}

	mismatch := httptest.NewRecorder()
	engine.HandleServiceCallback(mismatch, callbackRequest(t, "worker", protocol.ServiceTaskResponse{
		TaskID:         task.TaskID,
		Service:        task.Service,
		Status:         "completed",
		ProducerNodeID: "requester",
	}))
	if mismatch.Code != http.StatusForbidden {
		t.Fatalf("producer mismatch status = %d, want %d", mismatch.Code, http.StatusForbidden)
	}
	pending, ok := engine.GetTaskResponse(task.TaskID)
	if !ok || pending.Status != "pending" {
		t.Fatalf("producer mismatch changed task: %#v, exists=%v", pending, ok)
	}

	unknown := httptest.NewRecorder()
	engine.HandleServiceCallback(unknown, callbackRequest(t, "worker", protocol.ServiceTaskResponse{
		TaskID:  "unknown",
		Service: task.Service,
		Status:  "completed",
	}))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown task status = %d, want %d", unknown.Code, http.StatusNotFound)
	}

	valid := httptest.NewRecorder()
	engine.HandleServiceCallback(valid, callbackRequest(t, "worker", protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
		Outputs: map[string]any{
			protocol.OutputHashKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"nested":               map[string]any{"value": "original"},
		},
	}))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid callback status = %d, want %d", valid.Code, http.StatusOK)
	}
	stored, ok := engine.GetTaskResponse(task.TaskID)
	if !ok || stored.Status != protocol.TaskStatusIngesting || stored.ProducerNodeID != "worker" {
		t.Fatalf("authenticated callback stored %#v, exists=%v", stored, ok)
	}
}

func TestTaskCallbackAllowsOnlyBoundPipelineDelegates(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	schema := protocol.PipelineSchema{
		ID:      "delegated-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "middle", Service: "middle", TargetNodeID: "middle-worker"},
			{ID: "final", Service: "final", TargetNodeID: "final-worker"},
		},
	}
	if err := engine.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	task := protocol.TaskRequest{
		TaskID:                 "delegated-callback",
		Service:                schema.ID,
		ExpectedProducerNodeID: "middle-worker",
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      schema.ID,
			PipelineVersion: schema.Version,
		},
	}
	if err := engine.PreparePipelineTargets(&task); err != nil {
		t.Fatalf("prepare static delegates: %v", err)
	}
	engine.RegisterOutgoingTask(task)

	if err := engine.AcceptTaskCallback("unbound-worker", protocol.ServiceTaskResponse{
		TaskID: task.TaskID,
		Status: "completed",
	}); !errors.Is(err, ErrTaskProducer) {
		t.Fatalf("unbound producer error = %v, want ErrTaskProducer", err)
	}
	if err := engine.AcceptTaskCallback("final-worker", protocol.ServiceTaskResponse{
		TaskID: task.TaskID,
		Status: "completed",
	}); err != nil {
		t.Fatalf("bound final producer callback: %v", err)
	}
}

func TestTaskCallbackBindsDiscoveredPipelineDelegates(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	schema := protocol.PipelineSchema{
		ID:      "discovered-delegation",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "middle", Service: "middle"},
			{ID: "final", Service: "final"},
		},
	}
	if err := engine.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		return query.Service + "-worker", "", protocol.ServiceSchema{Name: query.Service}, nil
	})
	task := protocol.TaskRequest{
		TaskID:                 "discovered-delegation-callback",
		Service:                schema.ID,
		ExpectedProducerNodeID: "middle-worker",
		PipelineState: &protocol.PipelineExecutionState{
			PipelineID:      schema.ID,
			PipelineVersion: schema.Version,
		},
	}
	if err := engine.PreparePipelineTargets(&task); err != nil {
		t.Fatalf("prepare discovered delegates: %v", err)
	}
	engine.RegisterOutgoingTask(task)

	if err := engine.AcceptTaskCallback("final-worker", protocol.ServiceTaskResponse{
		TaskID: task.TaskID,
		Status: "completed",
	}); err != nil {
		t.Fatalf("discovered final producer callback: %v", err)
	}
}

func TestDynamicPipelineTargetSelectedOnceForDispatchAndCallbackAuthorization(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	schema := protocol.PipelineSchema{
		ID:      "single-selection",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "middle", Service: "middle", TargetNodeID: "middle-worker"},
			{ID: "final", Service: "final"},
		},
	}
	if err := engine.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}

	var finalSelections atomic.Int32
	engine.SetServiceFinder(func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
		if query.Service != "final" {
			t.Fatalf("unexpected dynamic selection for %q", query.Service)
		}
		if query.RequiredCapabilities[protocol.CapabilityPipelineState] != protocol.PipelineStateCapabilityVersion {
			t.Fatalf("final-step selection omitted pipeline-state capability: %#v", query.RequiredCapabilities)
		}
		call := finalSelections.Add(1)
		return fmt.Sprintf("final-worker-%d", call), "", protocol.ServiceSchema{Name: query.Service}, nil
	})

	task := protocol.TaskRequest{
		TaskID:                 "single-selection-task",
		Service:                schema.ID,
		RequesterNodeID:        "requester",
		ExpectedProducerNodeID: "middle-worker",
		Payload:                map[string]any{},
	}
	if err := engine.BindPipelineTask(&task); err != nil {
		t.Fatalf("bind pipeline task: %v", err)
	}
	task.PipelineState.CurrentStep = 1
	task.PipelineState.Outputs["middle"] = map[string]any{"value": "ready"}
	task.PipelineState.OutputProducers["middle"] = "middle-worker"
	if err := engine.PreparePipelineTargets(&task); err != nil {
		t.Fatalf("prepare pipeline targets: %v", err)
	}
	var localFinalRuns atomic.Int32
	if err := engine.RegisterNewService(
		protocol.ServiceSchema{Name: "final"},
		BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
			localFinalRuns.Add(1)
			return map[string]any{"unexpected": true}, nil
		}),
	); err != nil {
		t.Fatalf("register late local final service: %v", err)
	}
	engine.RegisterOutgoingTask(task)

	var dispatchedTo string
	engine.SetTaskDispatcher(func(targetPeerID string, forwarded protocol.TaskRequest) error {
		dispatchedTo = targetPeerID
		if forwarded.ExpectedProducerNodeID != "final-worker-1" {
			t.Fatalf("forwarded expected producer = %q, want final-worker-1", forwarded.ExpectedProducerNodeID)
		}
		return nil
	})
	engine.executePipelineStep(task, schema)

	if calls := finalSelections.Load(); calls != 1 {
		t.Fatalf("final step selected %d times, want exactly once", calls)
	}
	if localFinalRuns.Load() != 0 {
		t.Fatal("late local availability overrode the selected final target")
	}
	if dispatchedTo != "final-worker-1" {
		t.Fatalf("dispatched to %q, want first selected winner", dispatchedTo)
	}
	if err := engine.AcceptTaskCallback("final-worker-1", protocol.ServiceTaskResponse{
		TaskID: task.TaskID,
		Status: "completed",
	}); err != nil {
		t.Fatalf("selected final producer callback rejected: %v", err)
	}
}

func TestSubmitTaskHandlesDuplicateIDsBeforeQueueing(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	err := engine.RegisterNewService(protocol.ServiceSchema{Name: "dedupe"}, func(
		ctx context.Context,
		_ <-chan map[string]any,
		_ chan<- map[string]any,
		_ map[string]any,
	) (map[string]any, error) {
		if executions.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return map[string]any{"ok": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("register dedupe service: %v", err)
	}

	task := protocol.TaskRequest{
		TaskID:  "duplicate-before-queue",
		Service: "dedupe",
		Payload: map[string]any{"value": "same"},
	}
	if err := engine.SubmitTask(task); err != nil {
		t.Fatalf("submit first task: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not start")
	}
	if err := engine.SubmitTask(task); err != nil {
		t.Fatalf("exact duplicate should be idempotent: %v", err)
	}
	conflicting := protocol.CloneTaskRequest(task)
	conflicting.Payload["value"] = "different"
	if err := engine.SubmitTask(conflicting); !errors.Is(err, ErrTaskDuplicate) {
		t.Fatalf("conflicting duplicate error = %v, want ErrTaskDuplicate", err)
	}
	close(release)

	deadline := time.After(2 * time.Second)
	for {
		if response, ok := engine.GetTaskResponse(task.TaskID); ok && response.Status == "completed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("deduplicated task did not complete")
		default:
			runtime.Gosched()
		}
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("task executed %d times, want once", got)
	}
}

func TestPipelineForwardingReleasesAcceptedOwnership(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	task := protocol.TaskRequest{
		TaskID:  "forwarded-ownership",
		Service: "pipeline",
		PipelineState: &protocol.PipelineExecutionState{
			CurrentStep: 0,
		},
	}
	engine.acceptedTasks.Store(task.TaskID, protocol.CloneTaskRequest(task))
	dispatchCalled := false
	engine.SetTaskDispatcher(func(target string, forwarded protocol.TaskRequest) error {
		dispatchCalled = true
		if target != "worker" {
			t.Fatalf("dispatch target = %q, want worker", target)
		}
		if _, exists := engine.acceptedTasks.Load(task.TaskID); !exists {
			t.Fatal("accepted ownership released before forwarding succeeded")
		}
		if forwarded.ExpectedProducerNodeID != "worker" {
			t.Fatalf("expected producer = %q, want worker", forwarded.ExpectedProducerNodeID)
		}
		return nil
	})

	engine.routePipelineStep(task, protocol.PipelineStep{
		ID:           "remote",
		Service:      "remote-service",
		TargetNodeID: "worker",
	}, protocol.PipelineSchema{})
	if !dispatchCalled {
		t.Fatal("pipeline step was not forwarded")
	}
	if _, exists := engine.acceptedTasks.Load(task.TaskID); exists {
		t.Fatal("successful forwarding retained intermediate accepted ownership")
	}
}

func TestTaskStatusTransitionsPreserveIngestFailureAndTerminalState(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	task := protocol.TaskRequest{
		TaskID:                 "terminal",
		Service:                "pipeline",
		ExpectedProducerNodeID: "worker",
	}
	engine.RegisterOutgoingTask(task)
	if err := engine.AcceptTaskCallback("worker", protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
	}); err != nil {
		t.Fatalf("accept completion: %v", err)
	}

	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "failed",
		Error:   "ingest failed",
	})
	if err := engine.AcceptTaskCallback("worker", protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
	}); !errors.Is(err, ErrTaskTerminal) {
		t.Fatalf("late callback error = %v, want ErrTaskTerminal", err)
	}

	engine.Close()
	stored, ok := engine.GetTaskResponse(task.TaskID)
	if !ok || stored.Status != "failed" || stored.Error != "ingest failed" {
		t.Fatalf("terminal ingest failure overwritten: %#v, exists=%v", stored, ok)
	}
}

func TestComputeClosePreservesCompletedTaskStatus(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	task := protocol.TaskRequest{
		TaskID:                 "completed-before-close",
		Service:                "service",
		ExpectedProducerNodeID: "worker",
	}
	engine.RegisterOutgoingTask(task)
	if err := engine.AcceptTaskCallback("worker", protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
	}); err != nil {
		t.Fatalf("accept completion: %v", err)
	}
	engine.Close()

	stored, ok := engine.GetTaskResponse(task.TaskID)
	if !ok || stored.Status != "completed" {
		t.Fatalf("close overwrote completed status: %#v, exists=%v", stored, ok)
	}
}

func TestComputeDeepCopiesSchemaAndStatusBoundaries(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	schema := protocol.PipelineSchema{
		ID:          "copy-boundary",
		Version:     1,
		Steps:       []protocol.PipelineStep{{ID: "step", Service: "service"}},
		Connections: []protocol.PipelineConnection{{FromStep: "$initial", FromPort: "in", ToStep: "step", ToPort: "in"}},
	}
	if err := engine.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	schema.Steps[0].ID = "caller-mutated"
	schema.Connections[0].ToPort = "caller-mutated"
	first, ok := engine.GetPipeline("copy-boundary")
	if !ok || first.Steps[0].ID != "step" || first.Connections[0].ToPort != "in" {
		t.Fatalf("registration retained caller slices: %#v, exists=%v", first, ok)
	}
	first.Steps[0].ID = "reader-mutated"
	second, _ := engine.GetPipeline("copy-boundary")
	if second.Steps[0].ID != "step" {
		t.Fatalf("GetPipeline exposed registered slices: %#v", second)
	}
	invalid := protocol.PipelineSchema{
		ID:      "invalid-registration",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "consumer", Service: "service"},
			{ID: "producer", Service: "service"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "producer", FromPort: "out", ToStep: "consumer", ToPort: "in"},
		},
	}
	if err := engine.RegisterPipeline(invalid); err == nil {
		t.Fatal("registration boundary accepted non-topological schema")
	}
	if _, exists := engine.GetPipelineRevision(invalid.ID); exists {
		t.Fatal("invalid schema reached pipeline revision registry")
	}

	task := protocol.TaskRequest{
		TaskID:                 "copy-status",
		Service:                "service",
		ExpectedProducerNodeID: "worker",
	}
	engine.RegisterOutgoingTask(task)
	response := protocol.ServiceTaskResponse{
		TaskID:  task.TaskID,
		Service: task.Service,
		Status:  "completed",
		Outputs: map[string]any{"nested": map[string]any{"value": "original"}},
	}
	engine.RecordTaskResponse(response)
	response.Outputs["nested"].(map[string]any)["value"] = "caller-mutated"
	stored, _ := engine.GetTaskResponse(task.TaskID)
	if stored.Outputs["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("status retained caller map: %#v", stored)
	}
	stored.Outputs["nested"].(map[string]any)["value"] = "reader-mutated"
	again, _ := engine.GetTaskResponse(task.TaskID)
	if again.Outputs["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("GetTaskResponse exposed stored map: %#v", again)
	}
}

func TestSubmitTaskBindsPipelineBeforeQueueing(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := engine.RegisterNewService(protocol.ServiceSchema{Name: "block"}, func(
		context.Context, <-chan map[string]any, chan<- map[string]any, map[string]any,
	) (map[string]any, error) {
		close(started)
		<-release
		return map[string]any{}, nil
	}); err != nil {
		t.Fatalf("register blocker: %v", err)
	}
	if err := engine.SubmitTask(protocol.TaskRequest{TaskID: "block", Service: "block", Payload: map[string]any{}}); err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocker did not start")
	}

	schema := protocol.PipelineSchema{
		ID:      "queued-pipeline",
		Version: 1,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "missing"}},
	}
	if err := engine.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	request := protocol.TaskRequest{TaskID: "queued", Service: schema.ID, Payload: map[string]any{}}
	if err := engine.SubmitTask(request); err != nil {
		t.Fatalf("submit pipeline: %v", err)
	}
	accepted, ok := engine.acceptedTasks.Load(request.TaskID)
	if !ok || accepted.(protocol.TaskRequest).PipelineState == nil {
		t.Fatalf("pipeline was queued without bound state: %#v, exists=%v", accepted, ok)
	}

	newer := schema
	newer.Version++
	if err := engine.RegisterPipeline(newer); err != nil {
		t.Fatalf("register newer pipeline: %v", err)
	}
	close(release)
	requireEventuallyTaskStatus(t, engine, request.TaskID, "failed")
}

func TestPipelineMutationStagesDurablyUnderRevisionLock(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	schema := protocol.PipelineSchema{
		ID:      "durable",
		Version: 2,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "service"}},
	}
	if err := engine.ApplyPipelineRevision(schema, func(protocol.PipelineSchema) error {
		return errors.New("disk failed")
	}); err == nil {
		t.Fatal("expected durable staging failure")
	}
	if _, ok := engine.GetPipeline(schema.ID); ok {
		t.Fatal("failed durable staging changed memory")
	}

	if err := engine.ApplyPipelineRevision(schema, nil); err != nil {
		t.Fatalf("apply newer revision: %v", err)
	}
	var stalePersisted atomic.Bool
	older := schema
	older.Version--
	if err := engine.ApplyPipelineRevision(older, func(protocol.PipelineSchema) error {
		stalePersisted.Store(true)
		return nil
	}); err == nil {
		t.Fatal("expected stale revision rejection")
	}
	if stalePersisted.Load() {
		t.Fatal("stale revision reached durable staging")
	}
}

func requireEventuallyTaskStatus(t *testing.T, engine *ComputeEngine, taskID, status string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, ok := engine.GetTaskResponse(taskID)
		if ok && response.Status == status {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("task %s did not reach %s", taskID, status)
		case <-ticker.C:
		}
	}
}
