package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// outboxEntry is a durable pending gossip/notify payload.
type outboxEntry struct {
	ID        string          `json:"id"`
	PeerID    string          `json:"peer_id"`
	Kind      gossipKind      `json:"kind"` // see catalogKinds
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Server) outboxKey(peerID string, kind gossipKind, dedupe string) string {
	return peerID + "|" + string(kind) + "|" + dedupe
}

// notifyWithOutbox sends one gossip payload and durably queues it when the peer
// is unreachable (L2 SSOT for every catalog domain).
func (s *Server) notifyWithOutbox(ctx context.Context, peerID string, kind gossipKind, dedupe string, payload any, send func(ctx context.Context) error) error {
	err := send(ctx)
	if err != nil {
		s.Config.Logger.Debug("Peer notify failed, queued in outbox", "peerID", peerID, "kind", string(kind), "dedupe", dedupe, "error", err)
		s.enqueueOutbox(peerID, kind, dedupe, payload)
	}
	return err
}

func (s *Server) enqueueOutbox(peerID string, kind gossipKind, dedupe string, payload any) {
	if s.Storage == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	id := s.outboxKey(peerID, kind, dedupe)
	entry := outboxEntry{
		ID:        id,
		PeerID:    peerID,
		Kind:      kind,
		Payload:   raw,
		CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := s.Storage.PutOutboxRaw(id, data); err != nil {
		s.Config.Logger.Debug("Failed to enqueue notify outbox", "id", id, "error", err)
	}
}

// OutboxPendingCount returns durable pending notify count (tests / diagnostics).
func (s *Server) OutboxPendingCount() int {
	if s.Storage == nil {
		return 0
	}
	n, _ := s.Storage.CountOutboxEntries()
	return n
}

func (s *Server) startOutboxWorker() {
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.flushOutbox()
			}
		}
	}()
}

func (s *Server) flushOutbox() {
	if s.Storage == nil {
		return
	}
	rawEntries, err := s.Storage.ListOutboxRaw()
	if err != nil || len(rawEntries) == 0 {
		return
	}
	var wg sync.WaitGroup
	for id, raw := range rawEntries {
		var entry outboxEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			_ = s.Storage.DeleteOutboxEntry(id)
			continue
		}
		if entry.ID == "" {
			entry.ID = id
		}
		wg.Add(1)
		go func(entry outboxEntry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
			defer cancel()
			err := s.callPeer(ctx, entry.PeerID, func(ctx context.Context, peerID string) error {
				return s.deliverOutboxEntry(ctx, peerID, entry)
			})
			if err == nil {
				_ = s.Storage.DeleteOutboxEntry(entry.ID)
			}
		}(entry)
	}
	wg.Wait()
}

func (s *Server) deliverOutboxEntry(ctx context.Context, peerID string, entry outboxEntry) error {
	kind, ok := s.catalogKindFor(entry.Kind)
	if !ok || kind.deliver == nil {
		return fmt.Errorf("unknown outbox kind %q", entry.Kind)
	}
	return kind.deliver(s, ctx, peerID, entry.Payload)
}
