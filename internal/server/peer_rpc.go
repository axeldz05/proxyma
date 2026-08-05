package server

import (
	"context"
	"sync"
	"time"
)

// Named peer RPC timeouts (policy SSOT).
const (
	PeerRPCShort   = 1 * time.Second
	PeerRPCDefault = 2 * time.Second
	PeerRPCDiscover = 3 * time.Second
	PeerRPCSync    = 10 * time.Second
	PeerRPCBlob    = 30 * time.Second
	PeerRPCBlobLong = 2 * time.Minute
	PeerRPCStream  = 10 * time.Minute
	PeerRPCQUICWait = 8 * time.Second
)

// callPeer runs fn against one peer and updates online/offline liveness (L2).
func (s *Server) callPeer(ctx context.Context, peerID string, fn func(ctx context.Context, peerID string) error) error {
	err := fn(ctx, peerID)
	if err != nil {
		s.SetPeerOffline(peerID, err)
		return err
	}
	s.SetPeerOnline(peerID, true)
	return nil
}

type forEachPeerOpts struct {
	Timeout  time.Duration
	Parallel bool
	SkipSelf bool
}

// forEachPeer fans out fn across registered peers (L3).
func (s *Server) forEachPeer(opts forEachPeerOpts, fn func(ctx context.Context, peerID string) error) {
	if opts.Timeout <= 0 {
		opts.Timeout = PeerRPCDefault
	}
	peers := s.GetPeersCopy()
	if opts.Parallel {
		var wg sync.WaitGroup
		for peerID := range peers {
			if opts.SkipSelf && peerID == s.Config.ID {
				continue
			}
			wg.Add(1)
			go func(peerID string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
				defer cancel()
				_ = s.callPeer(ctx, peerID, fn)
			}(peerID)
		}
		wg.Wait()
		return
	}
	for peerID := range peers {
		if opts.SkipSelf && peerID == s.Config.ID {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
		_ = s.callPeer(ctx, peerID, fn)
		cancel()
	}
}
