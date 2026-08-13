package server

import (
	"context"
	"encoding/json"

	"proxyma/internal/compute"
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

// catalogKind is the SSOT for one gossip domain: how its entity is identified,
// reconciled against current state, pushed to a newly joined peer, and delivered.
// Registering a new domain means adding one entry here — no switch elsewhere.
//
// syncOnJoin may be nil for domains that reconcile through another channel
// (VFS uses manifest sync in ExecuteSync, not catalog push).
type catalogKind struct {
	Kind       gossipKind
	entityFrom func(raw json.RawMessage) (string, bool)
	current    func(s *Server, entity string) (payload any, keep bool, err error)
	syncOnJoin func(s *Server, peerID string)
	deliver    func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error
}

func (s *Server) catalogKinds() []catalogKind {
	return []catalogKind{
		{
			Kind: kindPipeline,
			entityFrom: func(raw json.RawMessage) (string, bool) {
				return notificationEntity(raw, func(n protocol.PipelineNotification) string {
					return n.Schema.ID
				})
			},
			current: func(s *Server, entity string) (any, bool, error) {
				pipelines, err := s.Storage.LoadPipelineSchemas()
				if err != nil {
					return nil, false, err
				}
				schema, ok := pipelines[entity]
				if !ok || schema.Version <= 0 {
					return nil, false, nil
				}
				action := protocol.ActionAdd
				if schema.Deleted {
					action = protocol.ActionRemove
				}
				return protocol.PipelineNotification{
					Action: action,
					NodeID: s.Config.ID,
					Schema: schema,
				}, true, nil
			},
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
			entityFrom: func(raw json.RawMessage) (string, bool) {
				return notificationEntity(raw, func(n protocol.ServiceNotification) string {
					return n.Schema.Name
				})
			},
			current: func(s *Server, entity string) (any, bool, error) {
				services, err := compute.LoadServicesMap(s.Config.StoragePath)
				if err != nil {
					return nil, false, err
				}
				if service, ok := services[entity]; ok {
					return protocol.ServiceNotification{
						Action: protocol.ActionAdd,
						NodeID: s.Config.ID,
						Schema: protocol.NormalizeServiceSchema(entity, service.Schema, service.Type),
					}, true, nil
				}
				return protocol.ServiceNotification{
					Action: protocol.ActionRemove,
					NodeID: s.Config.ID,
					Schema: protocol.ServiceSchema{Name: entity},
				}, true, nil
			},
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
			entityFrom: func(raw json.RawMessage) (string, bool) {
				return notificationEntity(raw, func(n protocol.PeerNotification) string {
					return n.File.Name
				})
			},
			current: func(s *Server, entity string) (any, bool, error) {
				file, exists, err := s.Storage.GetFileMetaE(entity)
				if err != nil {
					return nil, false, err
				}
				if !exists {
					return nil, false, nil
				}
				return protocol.PeerNotification{
					File:   file,
					Source: s.Config.Address,
				}, true, nil
			},
			deliver: func(s *Server, ctx context.Context, peerID string, raw json.RawMessage) error {
				return deliverNotification(ctx, raw, func(ctx context.Context, n protocol.PeerNotification) error {
					return s.peerClient.Notify(ctx, peerID, n)
				})
			},
		},
	}
}

func notificationEntity[T any](raw json.RawMessage, entity func(T) string) (string, bool) {
	var notification T
	if err := json.Unmarshal(raw, &notification); err != nil {
		return "", false
	}
	value := entity(notification)
	return value, value != ""
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
