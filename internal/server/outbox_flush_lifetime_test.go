package server_test

import (
	"context"
	"errors"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFlushOutboxDeliveryHonorsLifetimeContext pins the shutdown contract for
// outbox flush deliveries: an in-flight flush must observe cancellation from
// the server lifetime context instead of dying on its own timeout while
// ignoring shutdown.
func TestFlushOutboxDeliveryHonorsLifetimeContext(t *testing.T) {
	t.Parallel()

	const (
		phaseFail  = int32(0) // peer offline: synchronous notify fails and stages the entry
		phaseBlock = int32(1) // flush delivery parks inside the mock until ctx or release
	)
	var phase atomic.Int32
	var observedErr atomic.Value
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(ctx context.Context, peerID string, n protocol.ServiceNotification) error {
			if phase.Load() == phaseFail {
				return context.DeadlineExceeded
			}
			entered <- struct{}{}
			select {
			case <-ctx.Done():
				observedErr.Store(ctx.Err())
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	}

	sv := NewServer(t, testutil.DefaultConfig(t, "outbox-lifetime"), mock)
	sv.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://127.0.0.1:0"}})
	sv.SetPeerOnline("peer-b", true)

	sv.NotifyServiceToPeer("peer-b", protocol.ServiceSchema{Name: "ocr", Description: "lifetime-test"}, protocol.ActionAdd)
	require.Eventually(t, func() bool {
		return sv.OutboxPendingCount() >= 1
	}, 2*time.Second, 50*time.Millisecond, "failed notify must stage a durable outbox entry")

	phase.Store(phaseBlock)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("outbox flush never attempted delivery")
	}
	require.Equal(t, 1, sv.OutboxPendingCount(), "in-flight delivery must stay staged")

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- sv.Shutdown(ctx)
	}()

	require.Eventually(t, func() bool {
		err, _ := observedErr.Load().(error)
		return errors.Is(err, context.Canceled)
	}, 5*time.Second, 20*time.Millisecond)
	err, _ := observedErr.Load().(error)
	require.ErrorIs(t, err, context.Canceled,
		"in-flight flush delivery must be cancelled via the lifetime context")
	close(release)
	require.NoError(t, <-shutdownDone, "shutdown must join cancelled flush deliveries")
}
