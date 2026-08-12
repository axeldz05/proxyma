package compute

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"proxyma/internal/protocol"
)

const lifecycleTestTimeout = 2 * time.Second

type blockedComputeTask struct {
	engine   *ComputeEngine
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	abort    chan struct{}

	releaseOnce sync.Once
}

func newBlockedComputeTask(t *testing.T) *blockedComputeTask {
	t.Helper()

	task := &blockedComputeTask{
		engine:   NewComputeEngine(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 1, "lifecycle-test"),
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
		abort:    make(chan struct{}),
	}
	err := task.engine.RegisterNewService(protocol.ServiceSchema{
		Name:       "blocking",
		Parameters: map[string]protocol.ServiceParameter{},
	}, func(ctx context.Context, _ <-chan map[string]any, _ chan<- map[string]any, _ map[string]any) (map[string]any, error) {
		close(task.started)
		select {
		case <-ctx.Done():
			close(task.canceled)
			<-task.release
			return nil, ctx.Err()
		case <-task.abort:
			return nil, errors.New("test aborted")
		}
	})
	if err != nil {
		t.Fatalf("register blocking service: %v", err)
	}
	if err := task.engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "blocking-task",
		Service: "blocking",
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("submit blocking task: %v", err)
	}
	waitLifecycleSignal(t, task.started, "blocking task start")

	t.Cleanup(func() {
		task.releaseTask()
		close(task.abort)
		task.engine.Close()
	})
	return task
}

func (task *blockedComputeTask) releaseTask() {
	task.releaseOnce.Do(func() { close(task.release) })
}

func waitLifecycleSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(lifecycleTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestComputeCloseCancelsAndJoinsActiveTask(t *testing.T) {
	t.Parallel()

	task := newBlockedComputeTask(t)
	closed := make(chan struct{})
	go func() {
		task.engine.Close()
		close(closed)
	}()

	waitLifecycleSignal(t, task.canceled, "active task cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before the active task exited")
	default:
	}

	task.releaseTask()
	waitLifecycleSignal(t, closed, "compute close")

	task.engine.Close()
}

func TestComputeParentCancellationClosesEngine(t *testing.T) {
	t.Parallel()

	parent, cancelParent := context.WithCancel(context.Background())
	engine := NewComputeEngine(parent, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 1, "parent-lifecycle-test")
	t.Cleanup(engine.Close)

	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := engine.RegisterNewService(protocol.ServiceSchema{
		Name:       "parent-blocking",
		Parameters: map[string]protocol.ServiceParameter{},
	}, func(ctx context.Context, _ <-chan map[string]any, _ chan<- map[string]any, _ map[string]any) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}); err != nil {
		t.Fatalf("register parent-blocking service: %v", err)
	}
	if err := engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "parent-blocking-task",
		Service: "parent-blocking",
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("submit parent-blocking task: %v", err)
	}
	waitLifecycleSignal(t, started, "parent-blocking task start")

	cancelParent()
	waitLifecycleSignal(t, canceled, "parent context cancellation")

	if err := engine.SubmitTask(protocol.TaskRequest{
		TaskID:  "after-parent-cancel",
		Service: "parent-blocking",
	}); !errors.Is(err, ErrClosed) {
		t.Fatalf("SubmitTask after parent cancellation returned %v, want ErrClosed", err)
	}

	closed := make(chan struct{})
	go func() {
		engine.Close()
		close(closed)
	}()
	waitLifecycleSignal(t, closed, "parent-canceled engine close")
}

func TestSubmitTaskDuringCloseReturnsStableClosedError(t *testing.T) {
	t.Parallel()

	task := newBlockedComputeTask(t)
	closed := make(chan struct{})
	go func() {
		task.engine.Close()
		close(closed)
	}()
	waitLifecycleSignal(t, task.canceled, "compute close cancellation")

	const submitters = 64
	errs := make(chan error, submitters)
	var wg sync.WaitGroup
	wg.Add(submitters)
	for i := range submitters {
		go func(i int) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					errs <- errors.New("SubmitTask panicked during Close")
				}
			}()
			errs <- task.engine.SubmitTask(protocol.TaskRequest{
				TaskID:  "closing-task",
				Service: "blocking",
				Payload: map[string]any{"submitter": i},
			})
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != ErrClosed {
			t.Errorf("SubmitTask during Close returned %v, want stable ErrClosed", err)
		}
	}

	task.releaseTask()
	waitLifecycleSignal(t, closed, "compute close")
}

func TestComputeCloseFailsAcceptedSelectedAndQueuedTasks(t *testing.T) {
	t.Parallel()

	task := newBlockedComputeTask(t)
	accepted := []protocol.TaskRequest{
		{TaskID: "selected-task", Service: "blocking", Payload: map[string]any{}},
		{TaskID: "queued-task", Service: "blocking", Payload: map[string]any{}},
	}
	for _, req := range accepted {
		if err := task.engine.SubmitTask(req); err != nil {
			t.Fatalf("submit %s: %v", req.TaskID, err)
		}
	}

	closed := make(chan struct{})
	go func() {
		task.engine.Close()
		close(closed)
	}()
	waitLifecycleSignal(t, task.canceled, "active task cancellation")

	for _, req := range accepted {
		response, ok := task.engine.GetTaskResponse(req.TaskID)
		if !ok {
			t.Errorf("accepted task %s has no terminal status", req.TaskID)
			continue
		}
		if response.Status != "failed" || response.Error != ErrClosed.Error() {
			t.Errorf("accepted task %s status = %#v, want failed ErrClosed", req.TaskID, response)
		}
	}

	task.releaseTask()
	waitLifecycleSignal(t, closed, "compute close")
}
