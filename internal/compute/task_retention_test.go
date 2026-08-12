package compute

import (
	"errors"
	"testing"
	"time"

	"proxyma/internal/protocol"
)

func TestTaskStatusRetentionBoundsPendingAndTerminalEntries(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	now := time.Unix(1_000, 0)
	engine.taskStatusNow = func() time.Time { return now }
	engine.taskStatusLimit = 3
	engine.pendingTaskStatusTTL = time.Hour
	engine.terminalTaskStatusTTL = time.Hour

	for _, id := range []string{"pending-1", "pending-2", "pending-3"} {
		engine.RegisterOutgoingTask(protocol.TaskRequest{
			TaskID:                 id,
			Service:                "svc",
			ExpectedProducerNodeID: "worker",
		})
		now = now.Add(time.Second)
	}
	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  "terminal-new",
		Service: "svc",
		Status:  "completed",
	})

	statuses := engine.GetAllTaskStatuses()
	if len(statuses) > 2*engine.taskStatusLimit {
		t.Fatalf("retained %d task statuses, bounded maximum is %d", len(statuses), 2*engine.taskStatusLimit)
	}
	retired, ok := engine.GetTaskResponse("pending-1")
	if !ok || retired.Status != "failed" || retired.Error == "" {
		t.Fatalf("oldest pending status = %#v, exists=%v; want deterministic failure", retired, ok)
	}
	if err := engine.AcceptTaskCallback("worker", protocol.ServiceTaskResponse{
		TaskID:  "pending-1",
		Service: "svc",
		Status:  "completed",
	}); !errors.Is(err, ErrTaskTerminal) {
		t.Fatalf("late callback error = %v, want ErrTaskTerminal", err)
	}
	if _, ok := engine.GetTaskResponse("terminal-new"); !ok {
		t.Fatal("freshly stored terminal result was evicted")
	}
}

func TestTaskStatusRetentionUsesStatusSpecificAges(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	now := time.Unix(2_000, 0)
	engine.taskStatusNow = func() time.Time { return now }
	engine.taskStatusLimit = 10
	engine.pendingTaskStatusTTL = 10 * time.Second
	engine.terminalTaskStatusTTL = 20 * time.Second

	engine.RegisterOutgoingTask(protocol.TaskRequest{
		TaskID:                 "pending",
		Service:                "svc",
		ExpectedProducerNodeID: "worker",
	})
	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  "terminal",
		Service: "svc",
		Status:  "completed",
	})

	now = now.Add(11 * time.Second)
	engine.GetAllTaskStatuses()
	pending, ok := engine.GetTaskResponse("pending")
	if !ok || pending.Status != "failed" {
		t.Fatalf("expired pending status = %#v, exists=%v; want failed", pending, ok)
	}
	if terminal, ok := engine.GetTaskResponse("terminal"); !ok || terminal.Status != "completed" {
		t.Fatalf("terminal status = %#v, exists=%v; want retained completion", terminal, ok)
	}

	now = now.Add(21 * time.Second)
	statuses := engine.GetAllTaskStatuses()
	if len(statuses) != 0 {
		t.Fatalf("statuses = %#v, want expired terminal removed", statuses)
	}
}

func TestTaskStatusRetentionProtectsFreshlyPolledResult(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	now := time.Unix(3_000, 0)
	engine.taskStatusNow = func() time.Time { return now }
	engine.taskStatusLimit = 3
	engine.pendingTaskStatusTTL = time.Hour
	engine.terminalTaskStatusTTL = time.Hour

	for _, id := range []string{"terminal-polled", "terminal-old"} {
		engine.RecordTaskResponse(protocol.ServiceTaskResponse{
			TaskID:  id,
			Service: "svc",
			Status:  "completed",
		})
		now = now.Add(time.Second)
	}
	engine.RegisterOutgoingTask(protocol.TaskRequest{
		TaskID:                 "pending",
		Service:                "svc",
		ExpectedProducerNodeID: "worker",
	})

	now = now.Add(time.Second)
	if _, ok := engine.GetTaskResponse("terminal-polled"); !ok {
		t.Fatal("could not poll retained terminal result")
	}

	now = now.Add(time.Second)
	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  "terminal-new",
		Service: "svc",
		Status:  "completed",
	})

	if _, ok := engine.GetTaskResponse("terminal-polled"); !ok {
		t.Fatal("freshly polled result was evicted")
	}
	if _, ok := engine.GetTaskResponse("terminal-old"); ok {
		t.Fatal("older unpolled terminal result survived capacity eviction")
	}
}

func TestTaskStatusPollRenewsRetentionAge(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	now := time.Unix(4_000, 0)
	engine.taskStatusNow = func() time.Time { return now }
	engine.taskStatusLimit = 3
	engine.pendingTaskStatusTTL = 10 * time.Second
	engine.terminalTaskStatusTTL = 10 * time.Second
	engine.RecordTaskResponse(protocol.ServiceTaskResponse{
		TaskID:  "polled",
		Service: "svc",
		Status:  "completed",
	})

	now = now.Add(9 * time.Second)
	if _, ok := engine.GetTaskResponse("polled"); !ok {
		t.Fatal("poll dropped the requested result before returning it")
	}

	now = now.Add(9 * time.Second)
	statuses := engine.GetAllTaskStatuses()
	if len(statuses) != 1 || statuses[0].TaskID != "polled" {
		t.Fatalf("statuses = %#v, want poll-renewed result", statuses)
	}
}

func TestTaskStatusListingDoesNotRenewStaleWork(t *testing.T) {
	t.Parallel()

	engine := newReviewCompute(t, 1)
	now := time.Unix(5_000, 0)
	engine.taskStatusNow = func() time.Time { return now }
	engine.taskStatusLimit = 3
	engine.pendingTaskStatusTTL = 10 * time.Second
	engine.terminalTaskStatusTTL = time.Hour
	engine.RegisterOutgoingTask(protocol.TaskRequest{
		TaskID:                 "listed-pending",
		Service:                "svc",
		ExpectedProducerNodeID: "worker",
	})

	now = now.Add(9 * time.Second)
	if statuses := engine.GetAllTaskStatuses(); len(statuses) != 1 || statuses[0].Status != "pending" {
		t.Fatalf("statuses before TTL = %#v, want pending", statuses)
	}
	now = now.Add(2 * time.Second)
	response, ok := engine.GetTaskResponse("listed-pending")
	if !ok || response.Status != "failed" {
		t.Fatalf("status after listing and TTL = %#v, exists=%v; want failed", response, ok)
	}
}
