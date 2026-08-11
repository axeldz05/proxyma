package server

import (
	"context"
	"encoding/json"

	"proxyma/internal/protocol"
)

// gossipKind identifies one catalog domain propagated to peers. Persisted inside
// outbox entries, so values must stay stable across releases.
type gossipKind string

const (
	kindService  gossipKind = "service"
	kindPipeline gossipKind = "pipeline"
	kindVFS      gossipKind = "vfs"
)

// catalogKind is the SSOT for one gossip domain: how it is pushed to a peer that
// just joined and how a queued outbox payload is redelivered. Registering a new
// domain means adding one entry here — no switch elsewhere.
//
// syncOnJoin may be nil for domains that reconcile through another channel
// (VFS uses manifest sync in ExecuteSync, not catalog push).
type catalogKind struct {
	Kind       gossipKind
	syncOnJoin func(s *Server, peerID string)
	deliver    func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error
}

func (s *Server) catalogKinds() []catalogKind {
	return []catalogKind{
		{
			Kind: kindPipeline,
			syncOnJoin: func(s *Server, peerID string) {
				for _, schema := range s.Compute.ListPipelines() {
					s.NotifySchemaToPeer(peerID, schema, protocol.ActionAdd)
				}
			},
			deliver: func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error {
				return deliverNotification(ctx, raw, func(ctx context.Context, n protocol.PipelineNotification) error {
					return s.peerClient.NotifyPipelineSchema(ctx, peerID, n)
				})
			},
		},
		{
			Kind: kindService,
			syncOnJoin: func(s *Server, peerID string) {
				for _, name := range s.Compute.ListServices() {
					if schema, ok := s.Compute.GetService(name); ok {
						s.NotifyServiceToPeer(peerID, schema, protocol.ActionAdd)
					}
				}
			},
			deliver: func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error {
				return deliverNotification(ctx, raw, func(ctx context.Context, n protocol.ServiceNotification) error {
					return s.peerClient.NotifyServiceUpdate(ctx, peerID, n)
				})
			},
		},
		{
			Kind: kindVFS,
			deliver: func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error {
				return deliverNotification(ctx, raw, func(ctx context.Context, n protocol.PeerNotification) error {
					return s.peerClient.Notify(ctx, peerID, n)
				})
			},
		},
	}
}

// deliverNotification decodes a queued outbox payload and hands it to send (L1).
func deliverNotification[T any](ctx context.Context, raw json.RawMessage, send func(context.Context, T) error) error {
	var n T
	if err := json.Unmarshal(raw, &n); err != nil {
		return err
	}
	return send(ctx, n)
}

// catalogKindFor looks up a registered gossip domain.
func (s *Server) catalogKindFor(kind gossipKind) (catalogKind, bool) {
	for _, k := range s.catalogKinds() {
		if k.Kind == kind {
			return k, true
		}
	}
	return catalogKind{}, false
}

// syncCatalogToPeer pushes all registered catalog kinds to a newly joined peer.
func (s *Server) syncCatalogToPeer(peerID string) {
	for _, kind := range s.catalogKinds() {
		if kind.syncOnJoin == nil {
			continue
		}
		kind.syncOnJoin(s, peerID)
	}
}

// lookupCachedServiceSchema resolves a schema from local compute or peer cache without bidding (L2).
func (s *Server) lookupCachedServiceSchema(serviceName string) (protocol.ServiceSchema, bool) {
	if sc, ok := s.Compute.GetService(serviceName); ok {
		return sc, true
	}
	if s.Peers != nil {
		if sc, ok := s.Peers.GetServiceSchema(serviceName); ok {
			return sc, true
		}
	}
	return protocol.ServiceSchema{}, false
}

// resolveServiceBidTarget returns the peer that should run serviceName via cluster bid (L2).
// Pipelines always target self. Does not check local handlers (Stream uses GetHandler first).
func (s *Server) resolveServiceBidTarget(serviceName, sortStrategy string) (peerID string, err error) {
	if _, isPipeline := s.Compute.GetPipeline(serviceName); isPipeline {
		return s.Config.ID, nil
	}
	peerID, _, _, err = s.RequestServiceToCluster(protocol.DiscoveryQuery{
		Service:      serviceName,
		SortStrategy: protocol.NormalizeSortStrategy(sortStrategy),
	})
	return peerID, err
}
