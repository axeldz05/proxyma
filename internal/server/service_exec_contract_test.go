package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func TestLocalServiceRunRejectsMalformedOrNonObjectPayloadBeforeExecution(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{`{bad`, `[]`, `null`, `"text"`} {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			t.Parallel()

			srv := newLifecycleServer(t, &testutil.MockPeerClient{})
			var executions atomic.Int32
			err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
				Name:       "strict-unary",
				Type:       protocol.ServiceTypeScript,
				Parameters: map[string]protocol.ServiceParameter{},
			}, compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
				executions.Add(1)
				return map[string]any{"executed": true}, nil
			}))
			if err != nil {
				t.Fatalf("register service: %v", err)
			}

			if _, err := srv.LocalServiceRun("strict-unary", payload); err == nil {
				t.Fatalf("LocalServiceRun accepted payload %q", payload)
			}
			if got := executions.Load(); got != 0 {
				t.Fatalf("handler executed %d time(s) for payload %q", got, payload)
			}
		})
	}
}

func TestLocalServiceStreamRejectsMalformedPayloadBeforeExecution(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	var executions atomic.Int32
	err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "strict-stream",
		Type: protocol.ServiceTypeBidi,
	}, func(_ context.Context, _ <-chan map[string]any, out chan<- map[string]any, _ map[string]any) (map[string]any, error) {
		defer close(out)
		executions.Add(1)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("register service: %v", err)
	}

	if err := srv.LocalServiceStreamRun("strict-stream", `{bad`, nil); err == nil {
		t.Fatal("LocalServiceStreamRun accepted malformed payload")
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("stream handler executed %d time(s)", got)
	}
}

func TestLocalServiceRunWaitHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	started := make(chan struct{})
	err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:       "cancel-wait",
		Type:       protocol.ServiceTypeScript,
		Parameters: map[string]protocol.ServiceParameter{},
	}, compute.BuildUnaryHandler(func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if err != nil {
		t.Fatalf("register service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := srv.LocalServiceRunContext(ctx, "cancel-wait", `{}`)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("task did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("LocalServiceRunContext error = %v, want context.Canceled", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("task wait ignored caller cancellation")
	}
}
