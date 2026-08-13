package server

import (
	"encoding/json"
	"testing"

	"proxyma/internal/protocol"
)

// Every registered gossip domain must be fully described by catalogKinds so the
// outbox never needs a parallel kind switch.
func TestCatalogKindsAreComplete(t *testing.T) {
	s := &Server{}
	seen := map[gossipKind]bool{}

	for i, k := range s.catalogKinds() {
		if k.Kind == "" {
			t.Errorf("catalogKinds[%d] has an empty kind", i)
		}
		if seen[k.Kind] {
			t.Errorf("catalogKinds has duplicate kind %q", k.Kind)
		}
		seen[k.Kind] = true

		if k.entityFrom == nil {
			t.Errorf("catalog kind %q has no entity extractor", k.Kind)
		}
		if k.current == nil {
			t.Errorf("catalog kind %q has no current-payload reconciler", k.Kind)
		}
		if k.deliver == nil {
			t.Errorf("catalog kind %q has no deliver arm: outbox entries would be dropped", k.Kind)
		}
	}
}

// enqueueOutbox keys must stay unique per peer+kind+dedupe so retries dedupe
// instead of piling up.
func TestOutboxKeyIsScopedByKind(t *testing.T) {
	s := &Server{}
	a := s.outboxKey("peer-1", kindService, "ocr|add")
	b := s.outboxKey("peer-1", kindPipeline, "ocr|add")
	if a == b {
		t.Errorf("outbox keys collide across kinds: %q", a)
	}
	if got := s.outboxKey("peer-1", kindVFS, "f|h|1"); got == legacyOutboxKey("peer-1", kindVFS, "f|h|1") {
		t.Errorf("outbox key retained ambiguous legacy encoding: %q", got)
	}
}

func TestCatalogKindEntityExtractors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    gossipKind
		payload any
		entity  string
	}{
		{
			name:    "service",
			kind:    kindService,
			payload: protocol.ServiceNotification{Schema: protocol.ServiceSchema{Name: "ocr"}},
			entity:  "ocr",
		},
		{
			name:    "pipeline",
			kind:    kindPipeline,
			payload: protocol.PipelineNotification{Schema: protocol.PipelineSchema{ID: "prepare"}},
			entity:  "prepare",
		},
		{
			name:    "vfs",
			kind:    kindVFS,
			payload: protocol.PeerNotification{File: protocol.IndexEntry{Name: "document.txt"}},
			entity:  "document.txt",
		},
	}
	s := &Server{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			spec, ok := s.catalogKindFor(tt.kind)
			if !ok {
				t.Fatalf("catalog kind %q not registered", tt.kind)
			}
			entity, ok := spec.entityFrom(raw)
			if !ok || entity != tt.entity {
				t.Fatalf("entity = %q, ok=%v; want %q, true", entity, ok, tt.entity)
			}
			if entity, ok := spec.entityFrom(json.RawMessage("{")); ok || entity != "" {
				t.Fatalf("invalid payload entity = %q, ok=%v; want empty, false", entity, ok)
			}
		})
	}
}

func TestCatalogKindUnknownFailsClosed(t *testing.T) {
	t.Parallel()
	s := &Server{}

	if _, ok := s.catalogKindFor(gossipKind("unknown")); ok {
		t.Fatal("unknown gossip kind was registered")
	}
	if _, keep, err := s.currentNotificationPayload(gossipKind("unknown"), "entity"); err == nil || keep {
		t.Fatalf("unknown current payload keep=%v, err=%v; want false and error", keep, err)
	}
}
