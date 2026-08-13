package server

import (
	"testing"
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
