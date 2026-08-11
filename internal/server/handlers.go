package server

import (
	"net/http"
	"proxyma/internal/protocol"
)

// authMode is the mTLS policy applied to one inter-node HTTP route.
type authMode int

const (
	// authMTLS is the default: a certificate whose CN is a registered peer.
	authMTLS authMode = iota
	// authAnonymous skips the guard entirely (bootstrap paths reachable before pairing).
	authAnonymous
	// authMTLSUnregistered requires a certificate but tolerates a peer that is
	// not in the registry yet (it is announcing itself right now).
	authMTLSUnregistered
)

// httpRoute declares one mounted endpoint together with its auth policy, so route,
// mTLS guard and relay allowlist all read the same table (SSOT).
type httpRoute struct {
	Method  string
	Path    string
	Handler http.HandlerFunc
	Auth    authMode
	// RelayAnon allows a caller without a peer certificate to reach this path
	// through /relay/forward. Only the pairing handshake needs it.
	RelayAnon bool
}

func (s *Server) httpRoutes() []httpRoute {
	return []httpRoute{
		// Storage (StorageEngine).
		{Method: http.MethodPost, Path: protocol.PathUpload, Handler: s.Storage.HandleUpload},
		{Method: http.MethodGet, Path: protocol.PathDownloadPrefix, Handler: s.Storage.HandleDownload},
		{Method: http.MethodDelete, Path: protocol.PathFile, Handler: s.Storage.HandleDelete},
		{Method: http.MethodGet, Path: protocol.PathManifest, Handler: s.Storage.HandleManifest},
		{Method: http.MethodPost, Path: protocol.PathSubscribe, Handler: s.Storage.HandleSubscribe},
		{Method: http.MethodPost, Path: protocol.PathNotify, Handler: s.Storage.HandleNotification},

		// Compute / catalog.
		{Method: http.MethodPost, Path: protocol.PathServicesBid, Handler: s.Compute.HandleServiceBid},
		{Method: http.MethodPost, Path: protocol.PathServicesSubmit, Handler: s.Compute.HandleServiceSubmit},
		{Method: http.MethodPost, Path: protocol.PathServicesStream, Handler: s.HandleServicesStream},
		{Method: http.MethodPost, Path: protocol.PathServicesCallback, Handler: s.Compute.HandleServiceCallback},
		{Method: http.MethodPost, Path: protocol.PathServicesNotify, Handler: s.HandleServiceNotify},
		{Method: http.MethodGet, Path: protocol.PathServices, Handler: s.HandleGetServices},
		{Method: http.MethodPost, Path: protocol.PathSchemasNotify, Handler: s.HandleSchemaNotify},

		// Topology.
		{Method: http.MethodGet, Path: protocol.PathPeers, Handler: s.GetPeers},
		{Method: http.MethodPost, Path: protocol.PathPeersAnnounce, Handler: s.HandleAnnounce, Auth: authMTLSUnregistered},
		{Method: http.MethodPost, Path: protocol.PathPeersAdd, Handler: s.HandleAddPeer},
		{Method: http.MethodPost, Path: protocol.PathPeersLeave, Handler: s.HandleLeavePeer},
		{Method: http.MethodPost, Path: protocol.PathPeersOffline, Handler: s.HandleOfflinePeer},
		{Method: http.MethodPost, Path: protocol.PathPeersInvite, Handler: s.HandleGenerateInvite},
		{Method: http.MethodPost, Path: protocol.PathPeersProbe, Handler: s.HandleProbe, Auth: authAnonymous},
		{Method: http.MethodPost, Path: protocol.PathClusterJoin, Handler: s.HandleClusterJoin, Auth: authAnonymous, RelayAnon: true},
		{Method: http.MethodPost, Path: protocol.PathClusterRotate, Handler: s.HandleClusterRotate},

		// Relay / NAT traversal.
		{Method: http.MethodGet, Path: protocol.PathRelayPoll, Handler: s.HandleRelayPoll},
		{Method: http.MethodPost, Path: protocol.PathRelayForward, Handler: s.HandleRelayForward, Auth: authAnonymous},
		{Method: http.MethodPost, Path: protocol.PathRelayReply, Handler: s.HandleRelayReply},
		{Method: http.MethodPost, Path: protocol.PathHolePunchInit, Handler: s.HandleHolePunchInit},
		{Method: http.MethodPost, Path: protocol.PathWebRTCSignal, Handler: s.HandleWebRTCSignal},

		{Method: http.MethodGet, Path: protocol.PathTelemetry, Handler: s.HandleTelemetry},
	}
}

// routePolicy is the per-path slice of httpRoute consulted on every request.
type routePolicy struct {
	auth      authMode
	relayAnon bool
}

// routeIndex memoizes the policy lookup. Only the policy is cached, never the
// handlers: httpRoutes builds bound method values, so rebuilding it per request
// allocated one closure per route on the mTLS hot path, and callers still get
// freshly bound handlers every time MountHandlers runs.
func (s *Server) routeIndex() map[string]routePolicy {
	s.routeIndexOnce.Do(func() {
		routes := s.httpRoutes()
		idx := make(map[string]routePolicy, len(routes))
		for _, r := range routes {
			idx[r.Path] = routePolicy{auth: r.Auth, relayAnon: r.RelayAnon}
		}
		s.routePolicies = idx
	})
	return s.routePolicies
}

// routeAuth returns the auth policy declared for path (authMTLS when unknown, so
// an unmounted, mistyped or subtree path never falls open).
func (s *Server) routeAuth(path string) authMode {
	return s.routeIndex()[path].auth
}

// relayAllowsAnonymous reports whether path may be relayed for a caller without
// a peer certificate.
func (s *Server) relayAllowsAnonymous(path string) bool {
	return s.routeIndex()[path].relayAnon
}

func (s *Server) MountHandlers() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.httpRoutes() {
		mux.HandleFunc(r.Method+" "+r.Path, r.Handler)
	}
	// Bandwidth wrap once here so httptest fixtures and ListenAndServe share the same stack.
	return s.wrapWithBandwidthCounting(s.mTLSGuard(mux))
}
