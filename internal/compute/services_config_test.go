package compute

import (
	"testing"

	"proxyma/internal/protocol"
)

// The protocol type table and the compute builder table must stay aligned: a type
// declared without a builder would only fail at service registration time.
func TestEveryServiceTypeHasBuilder(t *testing.T) {
	for _, typ := range protocol.KnownServiceTypes() {
		if _, ok := serviceTypeBuilders[typ]; !ok {
			t.Errorf("service type %q has no handler builder", typ)
		}
	}

	known := make(map[protocol.ServiceType]bool, len(protocol.KnownServiceTypes()))
	for _, typ := range protocol.KnownServiceTypes() {
		known[typ] = true
	}
	for typ := range serviceTypeBuilders {
		if !known[typ] {
			t.Errorf("builder registered for %q, which is not a canonical protocol type", typ)
		}
		if typ.Normalize() != typ {
			t.Errorf("builder keyed by alias %q; key it by the canonical type %q", typ, typ.Normalize())
		}
	}
}

func TestRequireHTTPExec(t *testing.T) {
	if err := requireHTTPExec("https://host/x", protocol.ServiceTypeWebRTC, "signaling URL"); err != nil {
		t.Errorf("https exec must be accepted: %v", err)
	}
	if err := requireHTTPExec("/usr/bin/thing", protocol.ServiceTypeServerStream, "exec URL"); err == nil {
		t.Error("local command must be rejected for http-only types")
	}
}

func TestBuildHandlerRejectsUnknownType(t *testing.T) {
	if _, err := BuildHandler(protocol.ServiceType("quantum"), "x"); err == nil {
		t.Error("unknown service type must be rejected")
	}
}
