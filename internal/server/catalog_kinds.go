package server

import "proxyma/internal/protocol"

// catalogKind registers join-sync for one catalog domain (L3).
// Typed notify L1 (notifyService / notifyPipeline) stay separate — receive semantics differ.
type catalogKind struct {
	syncOnJoin func(s *Server, peerID string)
}

func (s *Server) catalogKinds() []catalogKind {
	return []catalogKind{
		{
			syncOnJoin: func(s *Server, peerID string) {
				for _, schema := range s.Compute.ListPipelines() {
					s.NotifySchemaToPeer(peerID, schema, protocol.ActionAdd)
				}
			},
		},
		{
			syncOnJoin: func(s *Server, peerID string) {
				for _, name := range s.Compute.ListServices() {
					if schema, ok := s.Compute.GetService(name); ok {
						s.NotifyServiceToPeer(peerID, schema, protocol.ActionAdd)
					}
				}
			},
		},
	}
}

// syncCatalogToPeer pushes all registered catalog kinds to a newly joined peer.
func (s *Server) syncCatalogToPeer(peerID string) {
	for _, kind := range s.catalogKinds() {
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
