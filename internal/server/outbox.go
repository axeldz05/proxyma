package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
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

func legacyOutboxKey(peerID string, kind gossipKind, dedupe string) string {
	return peerID + "|" + string(kind) + "|" + dedupe
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

func notificationEntityFromPayload(kind gossipKind, raw json.RawMessage) (string, bool) {
	switch kind {
	case kindService:
		var notification protocol.ServiceNotification
		if err := json.Unmarshal(raw, &notification); err != nil || notification.Schema.Name == "" {
			return "", false
		}
		return notification.Schema.Name, true
	case kindPipeline:
		var notification protocol.PipelineNotification
		if err := json.Unmarshal(raw, &notification); err != nil || notification.Schema.ID == "" {
			return "", false
		}
		return notification.Schema.ID, true
	case kindVFS:
		var notification protocol.PeerNotification
		if err := json.Unmarshal(raw, &notification); err != nil || notification.File.Name == "" {
			return "", false
		}
		return notification.File.Name, true
	default:
		return "", false
	}
}

func (s *Server) matchingLegacyOutboxEntries(peerID string, kind gossipKind, entity string) (map[string][]byte, error) {
	matches := make(map[string][]byte)
	rawEntries, err := s.Storage.ListLegacyOutboxRaw()
	if err != nil {
		return nil, err
	}
	for id, raw := range rawEntries {
		var entry outboxEntry
		if json.Unmarshal(raw, &entry) != nil || entry.PeerID != peerID || entry.Kind != kind {
			continue
		}
		legacyEntity := entry.Entity
		ok := legacyEntity != ""
		if !ok {
			legacyEntity, ok = notificationEntityFromPayload(kind, entry.Payload)
		}
		if ok && legacyEntity == entity {
			matches[id] = raw
		}
	}
	return matches, nil
}

// notifyWithOutbox durably stages the latest generation before sending, then
// compare-deletes only that generation after acknowledgement (L2 SSOT).
func (s *Server) notifyWithOutbox(
	ctx context.Context,
	peerID string,
	kind gossipKind,
	dedupe string,
	payload any,
	send func(ctx context.Context) error,
) error {
	attempt, current, err := s.stageOutbox(peerID, kind, dedupe, payload)
	if err != nil {
		s.Config.Logger.Debug("Peer notify could not be queued", "peerID", peerID, "kind", string(kind), "dedupe", dedupe, "error", err)
		return err
	}
	if !current {
		return nil
	}
	err = s.deliverOutboxAttempt(ctx, attempt, send)
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

func (s *Server) deliverOutboxAttemptsAsync(attempts []outboxAttempt) {
	for _, attempt := range attempts {
		attempt := attempt
		s.goOwned(func() {
			ctx, cancel := context.WithTimeout(s.lifetimeCtx, PeerRPCDefault)
			defer cancel()
			_ = s.deliverOutboxAttempt(ctx, attempt, func(ctx context.Context) error {
				return s.callPeer(ctx, attempt.Entry.PeerID, func(ctx context.Context, peerID string) error {
					return s.deliverOutboxEntry(ctx, peerID, attempt.Entry)
				})
			})
		})
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
		if canonical, ok := notificationEntityFromPayload(kind, raw); ok {
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
	if canonical, ok := notificationEntityFromPayload(kind, raw); ok {
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
	superseded, err := s.matchingLegacyOutboxEntries(peerID, kind, entity)
	if err != nil {
		return outboxAttempt{}, false, err
	}
	applied, err := s.Storage.PutOutboxRawIfCurrentGeneration(id, generation, data, superseded)
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

func (s *Server) reconcileLegacyOutbox(rawEntries map[string][]byte) error {
	type candidate struct {
		entry  outboxEntry
		entity string
		rows   map[string][]byte
	}
	groups := make(map[string]candidate)
	for id, raw := range rawEntries {
		var entry outboxEntry
		if json.Unmarshal(raw, &entry) != nil {
			continue
		}
		entity := entry.Entity
		ok := entity != ""
		if !ok {
			entity, ok = notificationEntityFromPayload(entry.Kind, entry.Payload)
		}
		if !ok {
			continue
		}
		key := s.outboxKey(entry.PeerID, entry.Kind, entity)
		item := groups[key]
		if item.rows == nil {
			item = candidate{entry: entry, entity: entity, rows: make(map[string][]byte)}
		}
		item.rows[id] = raw
		groups[key] = item
	}
	for _, item := range groups {
		id := s.outboxKey(item.entry.PeerID, item.entry.Kind, item.entity)
		expectedCurrent, err := s.Storage.GetOutboxRaw(id)
		if err != nil {
			return err
		}
		payload, keep, err := s.currentNotificationPayload(item.entry.Kind, item.entity)
		if err != nil {
			return err
		}
		if !keep {
			if err := s.Storage.DeleteLegacyOutboxEntriesIfUnchanged(item.rows); err != nil {
				return err
			}
			continue
		}
		generation, reserved, err := s.Storage.ReserveOutboxGenerationIfUnchanged(id, expectedCurrent)
		if err != nil {
			return err
		}
		if !reserved {
			continue
		}
		rawPayload, err := json.Marshal(payload)
		if err != nil {
			_ = s.Storage.ReleaseOutboxGeneration(id, generation)
			return err
		}
		entry := outboxEntry{
			ID:         id,
			PeerID:     item.entry.PeerID,
			Kind:       item.entry.Kind,
			Entity:     item.entity,
			Generation: generation,
			Payload:    rawPayload,
			CreatedAt:  time.Now().UTC(),
		}
		rawEntry, err := json.Marshal(entry)
		if err != nil {
			_ = s.Storage.ReleaseOutboxGeneration(id, generation)
			return err
		}
		_, err = s.Storage.PutOutboxRawIfCurrentGeneration(id, generation, rawEntry, item.rows)
		_ = s.Storage.ReleaseOutboxGeneration(id, generation)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) currentNotificationPayload(kind gossipKind, entity string) (any, bool, error) {
	switch kind {
	case kindService:
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
	case kindPipeline:
		pipelines, err := s.Storage.LoadPipelineSchemas()
		if err != nil {
			return nil, false, err
		}
		if schema, ok := pipelines[entity]; ok {
			if schema.Version <= 0 {
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
		}
		return nil, false, nil
	case kindVFS:
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
	default:
		return nil, false, fmt.Errorf("unsupported legacy outbox kind %q", kind)
	}
}

func (s *Server) reconcileActiveOutbox(rawEntries map[string][]byte) error {
	for id, raw := range rawEntries {
		var entry outboxEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		entity := entry.Entity
		if entity == "" {
			var ok bool
			entity, ok = notificationEntityFromPayload(entry.Kind, entry.Payload)
			if !ok {
				continue
			}
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

	legacyEntries, err := s.Storage.ListLegacyOutboxRaw()
	if err != nil {
		return
	}
	if err := s.reconcileLegacyOutbox(legacyEntries); err != nil {
		s.Config.Logger.Debug("Failed to reconcile legacy outbox", "error", err)
		return
	}
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
		go func(attempt outboxAttempt) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
			defer cancel()
			_ = s.deliverOutboxAttempt(ctx, attempt, func(ctx context.Context) error {
				return s.callPeer(ctx, attempt.Entry.PeerID, func(ctx context.Context, peerID string) error {
					return s.deliverOutboxEntry(ctx, peerID, attempt.Entry)
				})
			})
		}(attempt)
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
