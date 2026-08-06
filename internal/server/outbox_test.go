package server_test

import (
	"context"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOfflineNotifyOutboxFlushesWhenPeerReturns(t *testing.T) {
	t.Parallel()

	var deliveries atomic.Int32
	var down atomic.Bool
	down.Store(true)

	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(ctx context.Context, peerID string, n protocol.ServiceNotification) error {
			if down.Load() {
				return context.DeadlineExceeded
			}
			deliveries.Add(1)
			return nil
		},
	}

	sv := NewServer(t, testutil.DefaultConfig(t, "outbox-node"), mock)
	sv.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://127.0.0.1:0"}})
	sv.SetPeerOnline("peer-b", true)

	schema := protocol.ServiceSchema{Name: "ocr", Description: "offline-test"}
	sv.NotifyServiceToPeer("peer-b", schema, protocol.ActionAdd)

	require.Eventually(t, func() bool {
		return sv.OutboxPendingCount() >= 1
	}, 2*time.Second, 50*time.Millisecond, "notify failure must enqueue outbox")
	require.Equal(t, int32(0), deliveries.Load())

	down.Store(false)
	sv.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b.invalid"}})
	sv.SetPeerOnline("peer-b", true)

	require.Eventually(t, func() bool {
		return deliveries.Load() >= 1 && sv.OutboxPendingCount() == 0
	}, 3*time.Second, 50*time.Millisecond)
	require.Equal(t, int32(1), deliveries.Load(), "exactly one successful delivery after heal")
}