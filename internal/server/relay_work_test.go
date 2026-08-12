package server

import (
	"context"
	"fmt"
	"net/http"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayWorkSlotsBoundAndJoin(t *testing.T) {
	t.Parallel()

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	srv := &Server{
		lifetimeCtx:   lifetime,
		acceptingWork: true,
	}
	rm := NewRelayManager(srv)

	started := make(chan struct{}, relayQueueSize)
	release := make(chan struct{})
	for range relayQueueSize {
		finish, ok := rm.beginWork(context.Background())
		if !ok {
			t.Fatal("relay work slot unexpectedly rejected")
		}
		if !srv.goOwned(func() {
			defer finish()
			started <- struct{}{}
			<-release
		}) {
			t.Fatal("lifecycle owner unexpectedly rejected relay work")
		}
	}
	for range relayQueueSize {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("relay work did not start")
		}
	}
	if got := len(rm.workSlots); got != relayQueueSize {
		t.Fatalf("active relay work = %d, want queue bound %d", got, relayQueueSize)
	}

	srv.stopAcceptingOwnedWork()
	cancelLifetime()
	waitDone := make(chan struct{})
	go func() {
		srv.workWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("relay work wait returned before active work finished")
	default:
	}

	close(release)
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay work wait did not join active work")
	}
	if _, ok := rm.beginWork(context.Background()); ok {
		t.Fatal("relay manager accepted work after stop")
	}
}

func TestProcessRelayRequestUsesLifetimeContext(t *testing.T) {
	t.Parallel()

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})
	srv := &Server{
		Config:      testutil.DefaultConfig(t, "relay-lifetime"),
		lifetimeCtx: lifetime,
		peerClient:  &testutil.MockPeerClient{},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(handlerStarted)
			<-r.Context().Done()
			close(handlerCanceled)
			w.WriteHeader(http.StatusServiceUnavailable)
		}),
	}

	done := make(chan struct{})
	go func() {
		srv.processRelayRequestContext(lifetime, "https://sponsor.invalid", protocol.RelayRequest{
			ReqID:  "lifetime",
			Method: http.MethodGet,
			Path:   "/blocked",
		})
		close(done)
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("relay handler did not start")
	}
	cancelLifetime()
	select {
	case <-handlerCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("relay handler did not observe server lifetime cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay request did not finish after cancellation")
	}
}

func TestRelayPollingDoesNotDrainPastActiveWorkBound(t *testing.T) {
	t.Parallel()

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	pollingCtx, cancelPolling := context.WithCancel(context.Background())
	t.Cleanup(cancelLifetime)
	t.Cleanup(cancelPolling)

	var pollCount atomic.Int32
	mock := &testutil.MockPeerClient{
		OnPollRelay: func(context.Context, string, string) (protocol.RelayRequest, error) {
			n := pollCount.Add(1)
			return protocol.RelayRequest{
				ReqID:  fmt.Sprintf("bounded-%d", n),
				Method: http.MethodGet,
				Path:   "/blocked",
			}, nil
		},
	}
	started := make(chan struct{}, relayQueueSize)
	release := make(chan struct{})
	srv := &Server{
		Config:        testutil.DefaultConfig(t, "relay-bound"),
		lifetimeCtx:   lifetime,
		peerClient:    mock,
		acceptingWork: true,
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusOK)
		}),
	}
	srv.Relays = NewRelayManager(srv)

	pollingDone := make(chan struct{})
	go func() {
		defer close(pollingDone)
		srv.StartRelayPolling(pollingCtx, "https://sponsor.invalid")
	}()

	for range relayQueueSize {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded relay handler did not start")
		}
	}
	if got := pollCount.Load(); got != relayQueueSize {
		t.Fatalf("polling drained %d requests with %d active slots", got, relayQueueSize)
	}
	if got := len(srv.Relays.workSlots); got != relayQueueSize {
		t.Fatalf("active relay work = %d, want %d", got, relayQueueSize)
	}

	cancelLifetime()
	close(release)
	select {
	case <-pollingDone:
	case <-time.After(2 * time.Second):
		t.Fatal("bounded relay polling did not stop")
	}
	srv.stopAcceptingOwnedWork()
	waitDone := make(chan struct{})
	go func() {
		srv.workWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle owner did not join relay handlers")
	}
}
