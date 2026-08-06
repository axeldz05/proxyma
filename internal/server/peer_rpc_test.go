package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestMapEachPeerContinuesAfterPartialFailure(t *testing.T) {
	t.Parallel()

	cfg := testutil.DefaultConfig(t, "fanout-root")
	mock := &testutil.MockPeerClient{}
	s := New(cfg, mock)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	s.AddPeer("good-a", protocol.AddressRecord{Addresses: []string{"https://a"}})
	s.AddPeer("bad-b", protocol.AddressRecord{Addresses: []string{"https://b"}})
	s.AddPeer("good-c", protocol.AddressRecord{Addresses: []string{"https://c"}})
	s.SetPeerOnline("good-a", true)
	s.SetPeerOnline("bad-b", true)
	s.SetPeerOnline("good-c", true)

	results := mapEachPeer(s, forEachPeerOpts{Timeout: PeerRPCShort, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) (string, error) {
		if peerID == "bad-b" {
			return "", errors.New("boom")
		}
		return peerID + "-ok", nil
	})

	require.Len(t, results, 2)
	require.ElementsMatch(t, []string{"good-a-ok", "good-c-ok"}, results)

	// failed peer should be marked offline via callPeer
	require.False(t, s.Peers.IsPeerOnline("bad-b"))
	require.True(t, s.Peers.IsPeerOnline("good-a"))
	require.True(t, s.Peers.IsPeerOnline("good-c"))
}

func TestForEachPeerSkipSelf(t *testing.T) {
	t.Parallel()

	cfg := testutil.DefaultConfig(t, "self-node")
	s := New(cfg, &testutil.MockPeerClient{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	s.AddPeer(cfg.ID, protocol.AddressRecord{Addresses: []string{"https://self"}})
	s.AddPeer("other", protocol.AddressRecord{Addresses: []string{"https://other"}})

	var visited atomic.Int32
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCShort, Parallel: false, SkipSelf: true}, func(ctx context.Context, peerID string) error {
		require.NotEqual(t, cfg.ID, peerID)
		visited.Add(1)
		return nil
	})
	require.Equal(t, int32(1), visited.Load())
}
