package server

import (
	"net/http"
	"strings"
	"testing"

	"proxyma/internal/protocol"
)

// The route table is the only place declaring auth policy. This test pins the
// exceptions so widening them is a deliberate, visible edit.
func TestRouteAuthPolicy(t *testing.T) {
	s := &Server{}

	anonymous := map[string]bool{
		protocol.PathClusterJoin:  true,
		protocol.PathRelayForward: true,
		protocol.PathPeersProbe:   true,
	}
	relayAnon := map[string]bool{
		protocol.PathClusterJoin: true,
	}

	seen := map[string]bool{}
	for _, r := range s.httpRoutes() {
		if r.Path == "" || r.Handler == nil {
			t.Errorf("route %q is incomplete", r.Path)
		}
		if r.Method == "" {
			t.Errorf("route %q has no method", r.Path)
		}
		if seen[r.Method+" "+r.Path] {
			t.Errorf("duplicate route %s %s", r.Method, r.Path)
		}
		seen[r.Method+" "+r.Path] = true

		if got := r.Auth == authAnonymous; got != anonymous[r.Path] {
			t.Errorf("route %q anonymous=%v, want %v", r.Path, got, anonymous[r.Path])
		}
		if r.RelayAnon != relayAnon[r.Path] {
			t.Errorf("route %q relayAnon=%v, want %v", r.Path, r.RelayAnon, relayAnon[r.Path])
		}
		if r.RelayAnon && r.Auth != authAnonymous {
			t.Errorf("route %q is relay-anonymous but not anonymous at the guard", r.Path)
		}
	}

	if got := s.routeAuth(protocol.PathPeersAnnounce); got != authMTLSUnregistered {
		t.Errorf("announce must tolerate unregistered peers, got %v", got)
	}
	if got := s.routeAuth("/unmounted/path"); got != authMTLS {
		t.Errorf("unknown paths must default to mTLS, got %v", got)
	}
	if s.relayAllowsAnonymous(protocol.PathRelayForward) {
		t.Error("relay forward must not be relayable without a certificate")
	}
}

// routeAuth matches paths exactly, but ServeMux treats a trailing slash as a
// subtree pattern, so a request to /download/<hash> never finds its route entry
// and falls back to authMTLS. That default is fail-closed, which means a subtree
// route declaring a laxer policy would silently not get it.
func TestSubtreeRoutesKeepDefaultPolicy(t *testing.T) {
	s := &Server{}
	for _, r := range s.httpRoutes() {
		if !strings.HasSuffix(r.Path, "/") {
			continue
		}
		if r.Auth != authMTLS || r.RelayAnon {
			t.Errorf("subtree route %q declares a policy the exact-match guard cannot apply; "+
				"give it a concrete path or teach routeIndex about prefixes", r.Path)
		}
	}
}

func TestHTTPRoutesUseDeclaredMethods(t *testing.T) {
	s := &Server{}
	allowed := map[string]bool{
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodDelete: true,
	}
	for _, r := range s.httpRoutes() {
		if !allowed[r.Method] {
			t.Errorf("route %q uses unsupported method %q", r.Path, r.Method)
		}
	}
}
