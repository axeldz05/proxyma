package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/storage"
)

// Named peer RPC timeouts (policy SSOT).
const (
	PeerRPCShort     = 1 * time.Second
	PeerRPCDefault   = 2 * time.Second
	PeerRPCDiscover  = 3 * time.Second
	PeerRPCSync      = protocol.RPCTimeoutSync
	PeerRPCBlob      = 30 * time.Second
	PeerRPCBlobLong  = 2 * time.Minute
	PeerRPCStream    = 10 * time.Minute
	PeerRPCQUICWait  = protocol.HolePunchWait
	PeerRPCRelayHold = 60 * time.Second // long-poll hold on relay
	PeerRPCRelayTick = 15 * time.Second // client relay poll interval
	PeerRPCProbe     = 2 * time.Second  // TCP dial for HandleProbe (≠ DialTimeoutRouteProbe)
	PeerRPCSTUN      = 5 * time.Second  // STUN / NAT probe
	TaskWaitTimeout  = protocol.RPCTimeoutTaskWait
)

// errPeerSkipped means fn intentionally did not contact the peer.
// callPeer must not update online/offline liveness for skips.
var errPeerSkipped = errors.New("peer skipped")

// callPeer runs fn against one peer and updates online/offline liveness (L2).
func (s *Server) callPeer(ctx context.Context, peerID string, fn func(ctx context.Context, peerID string) error) error {
	err := fn(ctx, peerID)
	if err != nil {
		if errors.Is(err, errPeerSkipped) {
			return err
		}
		// Peer responded; blob was dropped locally due to VFS race — not unreachable.
		if errors.Is(err, storage.ErrBlobDiscarded) {
			s.SetPeerOnline(peerID, true)
			return err
		}
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
	_ = mapEachPeer(s, opts, func(ctx context.Context, peerID string) (struct{}, error) {
		return struct{}{}, fn(ctx, peerID)
	})
}

// mapEachPeer fans out fn across peers and collects successful results (L3).
// Failed calls still update liveness via callPeer; only successful values are returned.
func mapEachPeer[T any](s *Server, opts forEachPeerOpts, fn func(ctx context.Context, peerID string) (T, error)) []T {
	if opts.Timeout <= 0 {
		opts.Timeout = PeerRPCDefault
	}
	peers := s.GetPeersCopy()
	var (
		mu      sync.Mutex
		results []T
		wg      sync.WaitGroup
	)
	run := func(peerID string) {
		if opts.SkipSelf && peerID == s.Config.ID {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
		defer cancel()
		var val T
		err := s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
			var callErr error
			val, callErr = fn(ctx, peerID)
			return callErr
		})
		if err != nil {
			return
		}
		mu.Lock()
		results = append(results, val)
		mu.Unlock()
	}
	if opts.Parallel {
		for peerID := range peers {
			wg.Add(1)
			go func(peerID string) {
				defer wg.Done()
				run(peerID)
			}(peerID)
		}
		wg.Wait()
		return results
	}
	for peerID := range peers {
		run(peerID)
	}
	return results
}

// firstPeer runs fn across peers sequentially and returns the first successful result (L3).
// Failed calls still update liveness via callPeer.
func firstPeer[T any](s *Server, opts forEachPeerOpts, fn func(ctx context.Context, peerID string) (T, error)) (T, bool) {
	if opts.Timeout <= 0 {
		opts.Timeout = PeerRPCDefault
	}
	var zero T
	for peerID := range s.GetPeersCopy() {
		if opts.SkipSelf && peerID == s.Config.ID {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
		var val T
		err := s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
			var callErr error
			val, callErr = fn(ctx, peerID)
			return callErr
		})
		cancel()
		if err == nil {
			return val, true
		}
	}
	return zero, false
}

// gossipToPeer runs fn against one peer with PeerRPCDefault timeout (L2).
func (s *Server) gossipToPeer(peerID string, fn func(ctx context.Context, peerID string) error) {
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()
	_ = s.callPeer(ctx, peerID, fn)
}

// gossipAll fans out fn to all peers in parallel with PeerRPCDefault (L2).
func (s *Server) gossipAll(fn func(ctx context.Context, peerID string) error) {
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault, Parallel: true}, fn)
}
