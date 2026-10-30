package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// outboxEntry is a durable pending gossip/notify payload.
type outboxEntry struct {
	ID         string          `json:"id"`
	PeerID     string          `json:"peer_id"`
	Kind       gossipKind      `json:"kind"` // see catalogKinds
	Entity     string          `json:"entity,omitempty"`
	Generation uint64          `json:"generation,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
}

type outboxAttempt struct {
	StorageID string
	LockID    string
	Raw       []byte
	Entry     outboxEntry
	Durable   bool
}

type stagedOutboxMutation struct {
	server   *Server
	kind     gossipKind
	entity   string
	attempts []outboxAttempt
	unlocks  []func()
}

type outboxDeliveryLock struct {
	mu   sync.Mutex
	refs int
}

var outboxDeliveryLocks = struct {
	sync.Mutex
	locks map[struct {
		server *Server
		id     string
	}]*outboxDeliveryLock
}{
	locks: make(map[struct {
		server *Server
		id     string
	}]*outboxDeliveryLock),
}

func encodeOutboxTuple(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}

func (s *Server) outboxKey(peerID string, kind gossipKind, dedupe string) string {
	return encodeOutboxTuple(peerID, string(kind), dedupe)
}

func (s *Server) lockOutboxDelivery(id string) func() {
	key := struct {
		server *Server
		id     string
	}{server: s, id: id}
	outboxDeliveryLocks.Lock()
	lock := outboxDeliveryLocks.locks[key]
	if lock == nil {
		lock = &outboxDeliveryLock{}
		outboxDeliveryLocks.locks[key] = lock
	}
	lock.refs++
	outboxDeliveryLocks.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		outboxDeliveryLocks.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(outboxDeliveryLocks.locks, key)
		}
		outboxDeliveryLocks.Unlock()
	}
}

func (s *Server) notificationEntityFromPayload(kind gossipKind, raw json.RawMessage) (string, bool) {
	spec, ok := s.catalogKindFor(kind)
	if !ok || spec.entityFrom == nil {
		return "", false
	}
	return spec.entityFrom(raw)
}

// outboxEntity resolves the routing entity of a queued entry, falling back to
// the kind's payload extractor if the Entity field is empty.
func (s *Server) outboxEntity(kind gossipKind, raw json.RawMessage, stored string) (string, bool) {
	if stored != "" {
		return stored, true
	}
	return s.notificationEntityFromPayload(kind, raw)
}

// notifyWithOutbox durably stages the latest generation before sending, then
// compare-deletes only that generation after acknowledgement (L2 SSOT).
func (s *Server) notifyWithOutbox(
	ctx context.Context,
	peerID string,
	kind gossipKind,
	dedupe string,
	payload any,
) error {
	attempt, current, err := s.stageOutbox(peerID, kind, dedupe, payload)
	if err != nil {
		s.Config.Logger.Debug("Peer notify could not be queued", "peerID", peerID, "kind", string(kind), "dedupe", dedupe, "error", err)
		return err
	}
	if !current {
		return nil
	}
	err = s.deliverOutboxAttempt(ctx, attempt, func(ctx context.Context) error {
		return s.deliverOutboxEntry(ctx, peerID, attempt.Entry)
	})
	if err != nil {
		s.Config.Logger.Debug("Peer notify failed, queued in outbox", "peerID", peerID, "kind", string(kind), "dedupe", dedupe, "error", err)
	}
	return err
}

func (s *Server) enqueueOutbox(peerID string, kind gossipKind, dedupe string, payload any) {
	_, _, _ = s.stageOutbox(peerID, kind, dedupe, payload)
}

func (s *Server) prepareOutboxMutation(
	kind gossipKind,
	entity string,
	payload any,
) (*stagedOutboxMutation, error) {
	peerIDs := make([]string, 0, len(s.GetPeersRecordCopy()))
	for peerID := range s.GetPeersRecordCopy() {
		if peerID != s.Config.ID {
			peerIDs = append(peerIDs, peerID)
		}
	}
	sort.Strings(peerIDs)
	staged := &stagedOutboxMutation{server: s, kind: kind, entity: entity}
	for _, peerID := range peerIDs {
		lockID := s.outboxKey(peerID, kind, entity)
		staged.unlocks = append(staged.unlocks, s.lockOutboxDelivery(lockID))
		attempt, current, err := s.stageOutbox(peerID, kind, entity, payload)
		if err != nil {
			_ = staged.finish(false)
			return nil, err
		}
		if current {
			staged.attempts = append(staged.attempts, attempt)
		}
	}
	return staged, nil
}

func (m *stagedOutboxMutation) finish(committed bool) error {
	if m == nil {
		return nil
	}
	attempts := m.attempts
	var reconcileErr error
	if !committed {
		attempts, reconcileErr = m.reconcileCurrent()
	}
	for i := len(m.unlocks) - 1; i >= 0; i-- {
		m.unlocks[i]()
	}
	m.unlocks = nil
	if reconcileErr == nil && len(attempts) != 0 {
		m.server.deliverOutboxAttemptsAsync(attempts)
	}
	return reconcileErr
}

func (m *stagedOutboxMutation) reconcileCurrent() ([]outboxAttempt, error) {
	payload, keep, err := m.server.currentNotificationPayload(m.kind, m.entity)
	if err != nil {
		return nil, err
	}
	reconciled := make([]outboxAttempt, 0, len(m.attempts))
	for _, attempt := range m.attempts {
		if !keep {
			if _, err := m.server.Storage.DeleteOutboxEntryIfUnchanged(attempt.StorageID, attempt.Raw); err != nil {
				return nil, err
			}
			continue
		}
		next, current, err := m.server.stageOutbox(
			attempt.Entry.PeerID,
			m.kind,
			m.entity,
			payload,
		)
		if err != nil {
			return nil, err
		}
		if current {
			reconciled = append(reconciled, next)
		}
	}
	return reconciled, nil
}

// deliverOutboxTracked spawns one lifetime-bound outbox delivery. The done
// callback always runs exactly once, also when shutdown already stopped
// accepting owned work (the entry stays staged for the next flush/restart).
func (s *Server) deliverOutboxTracked(attempt outboxAttempt, done func()) {
	if !s.goOwned(func() {
		defer done()
		ctx, cancel := context.WithTimeout(s.lifetimeCtx, PeerRPCDefault)
		defer cancel()
		_ = s.deliverOutboxAttempt(ctx, attempt, func(ctx context.Context) error {
			return s.callPeer(ctx, attempt.Entry.PeerID, func(ctx context.Context, peerID string) error {
				return s.deliverOutboxEntry(ctx, peerID, attempt.Entry)
			})
		})
	}) {
		done()
	}
}

func (s *Server) deliverOutboxAttemptsAsync(attempts []outboxAttempt) {
	for _, attempt := range attempts {
		attempt := attempt
		s.deliverOutboxTracked(attempt, func() {})
	}
}

func (s *Server) stageOutbox(
	peerID string,
	kind gossipKind,
	entity string,
	payload any,
) (outboxAttempt, bool, error) {
	if s.Storage == nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return outboxAttempt{}, false, err
		}
		if canonical, ok := s.notificationEntityFromPayload(kind, raw); ok {
			entity = canonical
		}
		id := s.outboxKey(peerID, kind, entity)
		return outboxAttempt{
			StorageID: id,
			LockID:    id,
			Entry: outboxEntry{
				ID: id, PeerID: peerID, Kind: kind, Entity: entity, Payload: raw, CreatedAt: time.Now().UTC(),
			},
		}, true, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return outboxAttempt{}, false, err
	}
	if canonical, ok := s.notificationEntityFromPayload(kind, raw); ok {
		entity = canonical
	}
	id := s.outboxKey(peerID, kind, entity)
	generation, err := s.Storage.ReserveOutboxGeneration(id)
	if err != nil {
		return outboxAttempt{}, false, err
	}
	defer func() {
		_ = s.Storage.ReleaseOutboxGeneration(id, generation)
	}()
	entry := outboxEntry{
		ID:         id,
		PeerID:     peerID,
		Kind:       kind,
		Entity:     entity,
		Generation: generation,
		Payload:    raw,
		CreatedAt:  time.Now().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return outboxAttempt{}, false, err
	}
	applied, err := s.Storage.PutOutboxRawIfCurrentGeneration(id, generation, data)
	if err != nil {
		return outboxAttempt{}, false, err
	}
	return outboxAttempt{
		StorageID: id,
		LockID:    id,
		Raw:       data,
		Entry:     entry,
		Durable:   true,
	}, applied, nil
}

func (s *Server) deliverOutboxAttempt(ctx context.Context, attempt outboxAttempt, send func(context.Context) error) error {
	unlock := s.lockOutboxDelivery(attempt.LockID)
	defer unlock()
	if attempt.Durable {
		current, err := s.Storage.OutboxEntryMatches(attempt.StorageID, attempt.Raw)
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
	}
	if err := send(ctx); err != nil {
		return err
	}
	if attempt.Durable {
		if _, err := s.Storage.DeleteOutboxEntryIfUnchanged(attempt.StorageID, attempt.Raw); err != nil {
			return err
		}
	}
	return nil
}

// OutboxPendingCount returns durable pending notify count (tests / diagnostics).
func (s *Server) OutboxPendingCount() int {
	if s.Storage == nil {
		return 0
	}
	n, _ := s.Storage.CountOutboxEntries()
	return n
}

func (s *Server) startOutboxWorker() bool {
	return s.goOwned(func() {
		reconcile := func() {
			if err := s.Storage.ReconcileDownloadIntents(); err != nil {
				s.Config.Logger.Debug("Failed to reconcile durable download intents", "error", err)
			}
			s.flushOutbox()
		}
		reconcile()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				reconcile()
			}
		}
	})
}

func (s *Server) currentNotificationPayload(kind gossipKind, entity string) (any, bool, error) {
	spec, ok := s.catalogKindFor(kind)
	if !ok || spec.current == nil {
		return nil, false, fmt.Errorf("unsupported outbox kind %q", kind)
	}
	return spec.current(s, entity)
}

func (s *Server) reconcileActiveOutbox(rawEntries map[string][]byte) error {
	for id, raw := range rawEntries {
		var entry outboxEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		entity, ok := s.outboxEntity(entry.Kind, entry.Payload, entry.Entity)
		if !ok {
			continue
		}
		lockID := s.outboxKey(entry.PeerID, entry.Kind, entity)
		unlock := s.lockOutboxDelivery(lockID)
		payload, keep, err := s.currentNotificationPayload(entry.Kind, entity)
		if err != nil {
			unlock()
			return err
		}
		if !keep {
			_, err = s.Storage.DeleteOutboxEntryIfUnchanged(id, raw)
			unlock()
			if err != nil {
				return err
			}
			continue
		}
		currentPayload, err := json.Marshal(payload)
		if err != nil {
			unlock()
			return err
		}
		if bytes.Equal(entry.Payload, currentPayload) {
			unlock()
			continue
		}
		_, _, err = s.stageOutbox(entry.PeerID, entry.Kind, entity, payload)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) flushOutbox() {
	if s.Storage == nil {
		return
	}
	// Single-flight: overlapping ticker ticks must not double-deliver.
	if !s.outboxFlushMu.TryLock() {
		return
	}
	defer s.outboxFlushMu.Unlock()

	rawEntries, err := s.Storage.ListOutboxRaw()
	if err != nil || len(rawEntries) == 0 {
		return
	}
	if err := s.reconcileActiveOutbox(rawEntries); err != nil {
		s.Config.Logger.Debug("Failed to reconcile active outbox", "error", err)
		return
	}
	rawEntries, err = s.Storage.ListOutboxRaw()
	if err != nil || len(rawEntries) == 0 {
		return
	}
	var wg sync.WaitGroup
	for id, raw := range rawEntries {
		var entry outboxEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			_, _ = s.Storage.DeleteOutboxEntryIfUnchanged(id, raw)
			continue
		}
		if entry.ID == "" {
			entry.ID = id
		}
		lockID := id
		if entry.Entity != "" {
			lockID = s.outboxKey(entry.PeerID, entry.Kind, entry.Entity)
		}
		attempt := outboxAttempt{
			StorageID: id,
			LockID:    lockID,
			Raw:       raw,
			Entry:     entry,
			Durable:   true,
		}
		wg.Add(1)
		s.deliverOutboxTracked(attempt, wg.Done)
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
