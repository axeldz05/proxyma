package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func newOutboxRaceTestServer(t *testing.T, id string, mock *testutil.MockPeerClient) *Server {
	t.Helper()
	cfg := testutil.DefaultConfig(t, id)
	engine, err := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	s := &Server{
		Config:        cfg,
		Storage:       engine,
		peerClient:    mock,
		Peers:         NewPeerRegistry(cfg.Logger, cfg.ID),
		done:          make(chan struct{}),
		acceptingWork: true,
	}
	_, _ = s.Peers.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b"}})
	return s
}

func waitForOutboxSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func putLegacyOutboxEntry(t *testing.T, s *Server, id string, kind gossipKind, payload any, createdAt time.Time) {
	t.Helper()
	rawPayload, err := json.Marshal(payload)
	require.NoError(t, err)
	rawEntry, err := json.Marshal(outboxEntry{
		ID:        id,
		PeerID:    "peer-b",
		Kind:      kind,
		Payload:   rawPayload,
		CreatedAt: createdAt,
	})
	require.NoError(t, err)
	require.NoError(t, s.Storage.PutOutboxRaw(id, rawEntry))
}

func TestOutboxTupleKeysCannotCollideWithSeparatorsOrLegacyActions(t *testing.T) {
	t.Parallel()
	s := &Server{}

	left := s.outboxKey("a", kindService, "b|service|c")
	right := s.outboxKey("a|service|b", kindService, "c")
	require.NotEqual(t, left, right)

	canonicalSeparatorEntity := s.outboxKey("peer-b", kindService, "svc|add")
	legacyAdd := legacyOutboxKey("peer-b", kindService, "svc|add")
	require.NotEqual(t, canonicalSeparatorEntity, legacyAdd,
		"new entity keys must never alias an old action-suffixed key")
}

func TestFlushOutboxDoesNotDeleteNewerSameKeyPayload(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(context.Context, string, protocol.ServiceNotification) error {
			startedOnce.Do(func() { close(started) })
			<-release
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-compare-delete", mock)

	oldPayload := protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: protocol.ServiceSchema{Name: "svc", Description: "old"},
	}
	newPayload := protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: protocol.ServiceSchema{Name: "svc", Description: "new"},
	}
	s.enqueueOutbox("peer-b", kindService, "svc", oldPayload)

	flushed := make(chan struct{})
	go func() {
		s.flushOutbox()
		close(flushed)
	}()
	waitForOutboxSignal(t, started, "outbox delivery did not start")

	s.enqueueOutbox("peer-b", kindService, "svc", newPayload)
	close(release)
	waitForOutboxSignal(t, flushed, "outbox flush did not finish")

	require.Equal(t, 1, s.OutboxPendingCount(),
		"acknowledging the old snapshot must not delete a newer same-key payload")
	rawEntries, err := s.Storage.ListOutboxRaw()
	require.NoError(t, err)
	require.Len(t, rawEntries, 1)
	for _, raw := range rawEntries {
		var entry outboxEntry
		require.NoError(t, json.Unmarshal(raw, &entry))
		var notification protocol.ServiceNotification
		require.NoError(t, json.Unmarshal(entry.Payload, &notification))
		require.Equal(t, "new", notification.Schema.Description)
	}
}

func TestEnqueueOutboxAdvancesSameEntityGeneration(t *testing.T) {
	t.Parallel()
	s := newOutboxRaceTestServer(t, "outbox-generation", &testutil.MockPeerClient{})
	payload := protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: protocol.ServiceSchema{Name: "svc"},
	}

	s.enqueueOutbox("peer-b", kindService, "svc", payload)
	firstRaw, err := s.Storage.ListOutboxRaw()
	require.NoError(t, err)
	require.Len(t, firstRaw, 1)
	var first outboxEntry
	for _, raw := range firstRaw {
		require.NoError(t, json.Unmarshal(raw, &first))
	}

	payload.Schema.Description = "newer"
	s.enqueueOutbox("peer-b", kindService, "svc", payload)
	secondRaw, err := s.Storage.ListOutboxRaw()
	require.NoError(t, err)
	require.Len(t, secondRaw, 1)
	var second outboxEntry
	for _, raw := range secondRaw {
		require.NoError(t, json.Unmarshal(raw, &second))
	}

	require.NotZero(t, first.Generation)
	require.Greater(t, second.Generation, first.Generation)
}

func TestBlockedQueuedAddCompletesBeforeNewerRemove(t *testing.T) {
	t.Parallel()
	addStarted := make(chan struct{})
	releaseAdd := make(chan struct{})
	var (
		mu        sync.Mutex
		completed []string
	)
	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(_ context.Context, _ string, n protocol.ServiceNotification) error {
			if n.Action == protocol.ActionAdd {
				close(addStarted)
				<-releaseAdd
			}
			mu.Lock()
			completed = append(completed, n.Action)
			mu.Unlock()
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-delivery-order", mock)
	add := protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: protocol.ServiceSchema{Name: "svc"},
	}
	addAttempt, current, err := s.stageOutbox("peer-b", kindService, "svc", add)
	require.NoError(t, err)
	require.True(t, current)

	flushed := make(chan struct{})
	go func() {
		_ = s.deliverOutboxAttempt(context.Background(), addAttempt, func(ctx context.Context) error {
			return mock.NotifyServiceUpdate(ctx, "peer-b", add)
		})
		close(flushed)
	}()
	waitForOutboxSignal(t, addStarted, "queued add did not begin delivery")

	remove := protocol.ServiceNotification{
		Action: protocol.ActionRemove,
		NodeID: s.Config.ID,
		Schema: protocol.ServiceSchema{Name: "svc"},
	}
	attempt, current, err := s.stageOutbox("peer-b", kindService, "svc", remove)
	require.NoError(t, err)
	require.True(t, current)
	removeDone := make(chan error, 1)
	go func() {
		removeDone <- s.deliverOutboxAttempt(context.Background(), attempt, func(ctx context.Context) error {
			return mock.NotifyServiceUpdate(ctx, "peer-b", remove)
		})
	}()

	close(releaseAdd)
	waitForOutboxSignal(t, flushed, "queued add flush did not finish")
	require.NoError(t, <-removeDone)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{protocol.ActionAdd, protocol.ActionRemove}, completed,
		"newer remove must be delivered after an already-started older add")
	require.Zero(t, s.OutboxPendingCount())
}

func TestBlockedVFSAddCompletesBeforeNewerDelete(t *testing.T) {
	t.Parallel()
	addStarted := make(chan struct{})
	releaseAdd := make(chan struct{})
	var (
		mu        sync.Mutex
		completed []bool
	)
	mock := &testutil.MockPeerClient{
		OnNotify: func(_ context.Context, _ string, n protocol.PeerNotification) error {
			if !n.File.Deleted {
				close(addStarted)
				<-releaseAdd
			}
			mu.Lock()
			completed = append(completed, n.File.Deleted)
			mu.Unlock()
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-vfs-delivery-order", mock)
	add := protocol.PeerNotification{
		File:   protocol.IndexEntry{Name: "shared.txt", Hash: "old", Version: 1},
		Source: s.Config.Address,
	}
	addAttempt, current, err := s.stageOutbox("peer-b", kindVFS, "shared.txt|old|1", add)
	require.NoError(t, err)
	require.True(t, current)

	flushed := make(chan struct{})
	go func() {
		_ = s.deliverOutboxAttempt(context.Background(), addAttempt, func(ctx context.Context) error {
			return mock.Notify(ctx, "peer-b", add)
		})
		close(flushed)
	}()
	waitForOutboxSignal(t, addStarted, "queued VFS add did not begin delivery")

	remove := protocol.PeerNotification{
		File:   protocol.IndexEntry{Name: "shared.txt", Hash: "old", Version: 2, Deleted: true},
		Source: s.Config.Address,
	}
	attempt, current, err := s.stageOutbox("peer-b", kindVFS, "shared.txt|old|2|del", remove)
	require.NoError(t, err)
	require.True(t, current)
	removeDone := make(chan error, 1)
	go func() {
		removeDone <- s.deliverOutboxAttempt(context.Background(), attempt, func(ctx context.Context) error {
			return mock.Notify(ctx, "peer-b", remove)
		})
	}()

	close(releaseAdd)
	waitForOutboxSignal(t, flushed, "queued VFS add flush did not finish")
	require.NoError(t, <-removeDone)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []bool{false, true}, completed)
	require.Zero(t, s.OutboxPendingCount())
}

func TestOutboxWorkerIsOwnedAndJoined(t *testing.T) {
	t.Parallel()
	s := newOutboxRaceTestServer(t, "outbox-owned-worker", &testutil.MockPeerClient{})
	require.True(t, s.startOutboxWorker())
	s.stopAcceptingOwnedWork()

	joined := make(chan struct{})
	go func() {
		s.workWG.Wait()
		close(joined)
	}()
	select {
	case <-joined:
		t.Fatal("owned worker was not registered with server lifetime")
	default:
	}

	close(s.done)
	waitForOutboxSignal(t, joined, "owned outbox worker did not join after shutdown")
}

func TestLegacyQueuedAddReconcilesToCurrentRemovalBeforeReplay(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		actions []string
	)
	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(_ context.Context, _ string, n protocol.ServiceNotification) error {
			mu.Lock()
			actions = append(actions, n.Action)
			mu.Unlock()
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-legacy-reconcile", mock)
	schema := protocol.ServiceSchema{Name: "svc"}
	putLegacyOutboxEntry(t, s,
		legacyOutboxKey("peer-b", kindService, "svc|add"),
		kindService,
		protocol.ServiceNotification{Action: protocol.ActionAdd, NodeID: s.Config.ID, Schema: schema},
		time.Unix(1, 0),
	)
	s.flushOutbox()
	s.flushOutbox()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{protocol.ActionRemove}, actions,
		"stale legacy add must reconcile to the currently removed local entity")
	require.Zero(t, s.OutboxPendingCount())
}

func TestCatalogSupersessionDoesNotDeleteSeparatorNamedLegacyEntity(t *testing.T) {
	t.Parallel()
	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(context.Context, string, protocol.ServiceNotification) error {
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-legacy-collision", mock)
	separatorSchema := protocol.ServiceSchema{Name: "svc|add"}
	legacyID := legacyOutboxKey("peer-b", kindService, "svc|add")
	putLegacyOutboxEntry(t, s, legacyID, kindService, protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: separatorSchema,
	}, time.Unix(1, 0))

	require.NoError(t, s.notifyService(context.Background(), "peer-b",
		protocol.ServiceSchema{Name: "svc"}, protocol.ActionRemove))

	raw, err := s.Storage.ListLegacyOutboxRaw()
	require.NoError(t, err)
	require.Contains(t, raw, legacyID,
		"payload for entity svc|add must not be deleted while superseding entity svc")
}

func TestSuccessfulServiceRemoveSupersedesQueuedAdd(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		actions []string
	)
	mock := &testutil.MockPeerClient{
		OnNotifyServiceUpdate: func(_ context.Context, _ string, n protocol.ServiceNotification) error {
			mu.Lock()
			actions = append(actions, n.Action)
			mu.Unlock()
			if n.Action == protocol.ActionAdd {
				return errors.New("peer unavailable")
			}
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-service-order", mock)
	schema := protocol.ServiceSchema{Name: "svc"}

	require.Error(t, s.notifyService(context.Background(), "peer-b", schema, protocol.ActionAdd))
	require.Equal(t, 1, s.OutboxPendingCount())
	putLegacyOutboxEntry(t, s, legacyOutboxKey("peer-b", kindService, "svc|add"), kindService, protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: schema,
	}, time.Unix(1, 0))
	require.NoError(t, s.notifyService(context.Background(), "peer-b", schema, protocol.ActionRemove))
	s.flushOutbox()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{protocol.ActionAdd, protocol.ActionRemove}, actions,
		"a successful newer remove must prevent replay of the queued add")
	require.Zero(t, s.OutboxPendingCount())
}

func TestSuccessfulPipelineRemoveSupersedesQueuedAdd(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		actions []string
	)
	mock := &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(_ context.Context, _ string, n protocol.PipelineNotification) error {
			mu.Lock()
			actions = append(actions, n.Action)
			mu.Unlock()
			if n.Action == protocol.ActionAdd {
				return errors.New("peer unavailable")
			}
			return nil
		},
	}
	s := newOutboxRaceTestServer(t, "outbox-pipeline-order", mock)
	schema := protocol.PipelineSchema{ID: "pipeline"}

	require.Error(t, s.notifyPipeline(context.Background(), "peer-b", schema, protocol.ActionAdd))
	require.Equal(t, 1, s.OutboxPendingCount())
	putLegacyOutboxEntry(t, s, legacyOutboxKey("peer-b", kindPipeline, "pipeline|add"), kindPipeline, protocol.PipelineNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: schema,
	}, time.Unix(1, 0))
	require.NoError(t, s.notifyPipeline(context.Background(), "peer-b", schema, protocol.ActionRemove))
	s.flushOutbox()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{protocol.ActionAdd, protocol.ActionRemove}, actions,
		"a successful newer remove must prevent replay of the queued add")
	require.Zero(t, s.OutboxPendingCount())
}

func TestLocalServiceDoesNotCommitWhenOutboxWALFails(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "service-wal-failure")
	s, err := New(cfg, &testutil.MockPeerClient{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	s.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b"}})
	require.NoError(t, s.Storage.Close())

	_, err = s.LocalServiceAdd("wal-service", "", "printf '{}'", "", "", "", "")
	require.Error(t, err)
	_, registered := s.Compute.GetService("wal-service")
	require.False(t, registered)
	services, loadErr := compute.LoadServicesMap(cfg.StoragePath)
	require.NoError(t, loadErr)
	require.NotContains(t, services, "wal-service",
		"a mutation must not be acknowledged when its write-ahead intent cannot persist")
}

func TestLegacyPipelineWithoutRevisionDropsInvalidRemove(t *testing.T) {
	t.Parallel()
	var deliveries int
	s := newOutboxRaceTestServer(t, "legacy-pipeline-no-revision", &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(context.Context, string, protocol.PipelineNotification) error {
			deliveries++
			return nil
		},
	})
	putLegacyOutboxEntry(t, s,
		legacyOutboxKey("peer-b", kindPipeline, "missing|add"),
		kindPipeline,
		protocol.PipelineNotification{
			Action: protocol.ActionAdd,
			NodeID: s.Config.ID,
			Schema: protocol.PipelineSchema{ID: "missing", Version: 1},
		},
		time.Unix(1, 0),
	)

	s.flushOutbox()
	require.Zero(t, deliveries, "migration must not invent an invalid version-zero tombstone")
	require.Zero(t, s.OutboxPendingCount(), "unreconstructable legacy row must be safely reconciled")
}

func TestRestartReconcilesWriteAheadIntentBeforeReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := testutil.DefaultConfig(t, "wal-restart")
	cfg.StoragePath = dir
	firstStorage, err := storage.NewStorageEngine(cfg.Logger, dir, nil, nil)
	require.NoError(t, err)
	first := &Server{Config: cfg, Storage: firstStorage}
	add := protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: cfg.ID,
		Schema: protocol.ServiceSchema{Name: "never-committed"},
	}
	_, current, err := first.stageOutbox("peer-b", kindService, add.Schema.Name, add)
	require.NoError(t, err)
	require.True(t, current)
	require.NoError(t, firstStorage.Close())

	delivered := make(chan string, 1)
	restartedStorage, err := storage.NewStorageEngine(cfg.Logger, dir, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restartedStorage.Close()) })
	restarted := &Server{
		Config:  cfg,
		Storage: restartedStorage,
		peerClient: &testutil.MockPeerClient{OnNotifyServiceUpdate: func(_ context.Context, _ string, n protocol.ServiceNotification) error {
			delivered <- n.Action
			return nil
		}},
		Peers: NewPeerRegistry(cfg.Logger, cfg.ID),
		done:  make(chan struct{}),
	}
	_, _ = restarted.Peers.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b"}})

	restarted.flushOutbox()
	require.Equal(t, protocol.ActionRemove, <-delivered,
		"startup must reconcile an uncommitted write-ahead add to persisted state")
	require.Zero(t, restarted.OutboxPendingCount())
}

func TestFailedPipelineMutationCompensatesWriteAheadIntent(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "pipeline-wal-compensation")
	delivered := make(chan protocol.PipelineNotification, 1)
	s, err := New(cfg, &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(_ context.Context, _ string, n protocol.PipelineNotification) error {
			delivered <- n
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, s.Shutdown(ctx))
	})
	_, evicted := s.Peers.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b"}})
	require.Empty(t, evicted)
	require.NoError(t, s.Compute.RegisterNewService(
		protocol.ServiceSchema{Name: "service", Parameters: map[string]protocol.ServiceParameter{}},
		compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		}),
	))
	current := protocol.PipelineSchema{
		ID:      "cannot-downgrade",
		Version: 2,
		Steps:   []protocol.PipelineStep{{ID: "current", Service: "service"}},
	}
	require.NoError(t, s.Compute.RegisterPipeline(current))
	require.NoError(t, s.Storage.SavePipelineSchema(current))
	stale := current
	stale.Version = 1
	stale.Steps = []protocol.PipelineStep{{ID: "stale", Service: "service"}}
	rawStale, err := json.Marshal(stale)
	require.NoError(t, err)

	err = s.LocalPipelineAdd(string(rawStale))
	require.Error(t, err)
	got := <-delivered
	require.Equal(t, protocol.ActionAdd, got.Action)
	require.Equal(t, current.Version, got.Schema.Version)
	require.Eventually(t, func() bool {
		return s.OutboxPendingCount() == 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestPipelineZeroVersionNormalizesBeforeDurableStaging(t *testing.T) {
	t.Parallel()
	s := newOutboxRaceTestServer(t, "pipeline-version-normalize", &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(context.Context, string, protocol.PipelineNotification) error {
			return errors.New("offline")
		},
	})
	schema := protocol.PipelineSchema{ID: "zero-version"}

	require.Error(t, s.notifyPipeline(context.Background(), "peer-b", schema, protocol.ActionAdd))
	rawEntries, err := s.Storage.ListOutboxRaw()
	require.NoError(t, err)
	require.Len(t, rawEntries, 1)
	for _, raw := range rawEntries {
		var entry outboxEntry
		require.NoError(t, json.Unmarshal(raw, &entry))
		var notification protocol.PipelineNotification
		require.NoError(t, json.Unmarshal(entry.Payload, &notification))
		require.Equal(t, 1, notification.Schema.Version)
	}
}

func TestCommittedZeroVersionPipelineSurvivesRestartReconciliation(t *testing.T) {
	t.Parallel()
	s := newOutboxRaceTestServer(t, "pipeline-version-restart", &testutil.MockPeerClient{})
	schema := protocol.PipelineSchema{ID: "committed-zero"}
	require.NoError(t, s.Storage.SavePipelineSchema(schema))
	payload := protocol.PipelineNotification{
		Action: protocol.ActionAdd,
		NodeID: s.Config.ID,
		Schema: schema,
	}
	_, current, err := s.stageOutbox("peer-b", kindPipeline, schema.ID, payload)
	require.NoError(t, err)
	require.True(t, current)

	delivered := make(chan protocol.PipelineNotification, 1)
	s.peerClient = &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(_ context.Context, _ string, notification protocol.PipelineNotification) error {
			delivered <- notification
			return nil
		},
	}
	s.flushOutbox()

	var notification protocol.PipelineNotification
	select {
	case notification = <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("zero-version committed pipeline was deleted instead of reconciled")
	}
	require.Equal(t, 1, notification.Schema.Version)
	persisted, err := s.Storage.LoadPipelineSchemas()
	require.NoError(t, err)
	require.Equal(t, 1, persisted[schema.ID].Version)
	require.Zero(t, s.OutboxPendingCount())
}

func TestCorruptDownloadDoesNotHotLoopDurableIntent(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "corrupt-download")
	cfg.Workers = 1
	var attempts atomic.Int32
	firstAttempt := make(chan struct{})
	var firstOnce sync.Once
	client := &testutil.MockPeerClient{
		OnDownloadBlob: func(context.Context, string, string) (io.ReadCloser, error) {
			attempts.Add(1)
			firstOnce.Do(func() { close(firstAttempt) })
			return io.NopCloser(bytes.NewReader([]byte("corrupt"))), nil
		},
	}
	s, err := New(cfg, client)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, s.Shutdown(ctx))
	})
	s.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b"}})
	expected := sha256.Sum256([]byte("expected"))
	entry := protocol.IndexEntry{
		Name:    "corrupt.txt",
		Hash:    fmt.Sprintf("%x", expected),
		Version: 1,
	}
	require.NoError(t, s.Storage.SetSubscription(entry.Name, true))
	_, err = s.Storage.ProcessRemoteManifestFromSource(
		map[string]protocol.IndexEntry{entry.Name: entry},
		"peer-b",
	)
	require.NoError(t, err)
	waitForOutboxSignal(t, firstAttempt, "corrupt download did not start")
	require.Eventually(t, func() bool {
		return s.Storage.CountDownloadIntents() == 0
	}, 2*time.Second, 10*time.Millisecond)

	// Stop and join the sole worker so any retry already queued by the periodic
	// reconciler is accounted for before checking explicit reconciliation.
	s.cancelLife()
	s.downloadWG.Wait()
	attemptsAfterQuarantine := attempts.Load()
	for range 3 {
		require.NoError(t, s.Storage.ReconcileDownloadIntents())
	}
	require.Equal(t, attemptsAfterQuarantine, attempts.Load(),
		"quarantined integrity failure must not be retried after its durable intent is removed")
	require.LessOrEqual(t, attemptsAfterQuarantine, int32(2),
		"periodic reconciliation may queue at most one bounded duplicate before quarantine")
}
