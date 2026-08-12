package server

import (
	"testing"
)

// Every gossip domain must be fully described by the catalogKinds table: a stable
// kind string and a deliver arm, so the outbox never hits an unknown kind.
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

		if k.deliver == nil {
			t.Errorf("catalog kind %q has no deliver arm: outbox entries would be dropped", k.Kind)
		}
	}

	for _, want := range []gossipKind{kindService, kindPipeline, kindVFS} {
		if !seen[want] {
			t.Errorf("catalog kind %q is not registered", want)
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
