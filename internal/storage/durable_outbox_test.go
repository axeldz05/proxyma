package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

type failingUpsertIndex struct {
	IndexStore
	err error
}

func (s *failingUpsertIndex) Upsert(protocol.IndexEntry) (bool, error) {
	return false, s.err
}

func TestOutboxGenerationRejectsReverseCommitAndPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	engine, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)

	const id = "canonical-key"
	older, err := engine.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	newer, err := engine.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	require.Greater(t, newer, older)

	applied, err := engine.PutOutboxRawIfCurrentGeneration(id, newer, []byte("newer"), nil)
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = engine.PutOutboxRawIfCurrentGeneration(id, older, []byte("older"), nil)
	require.NoError(t, err)
	require.False(t, applied, "an older reservation must not overwrite a newer committed value")

	raw, err := engine.ListOutboxRaw()
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{id: []byte("newer")}, raw)
	count, err := engine.CountOutboxEntries()
	require.NoError(t, err)
	require.Equal(t, 1, count, "generation metadata must not count as pending delivery")
	require.NoError(t, engine.Close())

	reopened, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	afterRestart, err := reopened.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	require.Greater(t, afterRestart, newer)
}

func TestOutboxV2NamespaceCannotCollideWithLegacyKey(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	const id = "same-arbitrary-key"
	require.NoError(t, engine.PutOutboxRaw(id, []byte("legacy")))
	generation, err := engine.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	applied, err := engine.PutOutboxRawIfCurrentGeneration(id, generation, []byte("v2"), nil)
	require.NoError(t, err)
	require.True(t, applied)

	v2, err := engine.ListOutboxRaw()
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{id: []byte("v2")}, v2)
	legacy, err := engine.ListLegacyOutboxRaw()
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{id: []byte("legacy")}, legacy)
	count, err := engine.CountOutboxEntries()
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestOutboxReservationReleaseCannotDeleteNewerReservation(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	older, err := engine.ReserveOutboxGeneration("entity")
	require.NoError(t, err)
	newer, err := engine.ReserveOutboxGeneration("entity")
	require.NoError(t, err)
	require.NoError(t, engine.ReleaseOutboxGeneration("entity", older))
	applied, err := engine.PutOutboxRawIfCurrentGeneration("entity", newer, []byte("newer"), nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, engine.subscriptions.View(func(tx *bbolt.Tx) error {
		require.Zero(t, tx.Bucket([]byte(bucketNotifyOutboxV2Reservations)).Stats().KeyN)
		return nil
	}))
}

func TestLegacyMigrationSnapshotCannotOverwriteNewerV2Mutation(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	const id = "entity"
	require.NoError(t, engine.PutOutboxRaw("legacy", []byte("legacy")))
	expectedCurrent, err := engine.GetOutboxRaw(id)
	require.NoError(t, err)
	require.Nil(t, expectedCurrent)

	generation, err := engine.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	applied, err := engine.PutOutboxRawIfCurrentGeneration(id, generation, []byte("newer"), nil)
	require.NoError(t, err)
	require.True(t, applied)

	_, migrated, err := engine.ReserveOutboxGenerationIfUnchanged(id, expectedCurrent)
	require.NoError(t, err)
	require.False(t, migrated)
	current, err := engine.GetOutboxRaw(id)
	require.NoError(t, err)
	require.Equal(t, []byte("newer"), current)
}

func TestLegacyMigrationCannotStealInFlightMutationReservation(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	const id = "in-flight"
	mutationGeneration, err := engine.ReserveOutboxGeneration(id)
	require.NoError(t, err)
	_, reserved, err := engine.ReserveOutboxGenerationIfUnchanged(id, nil)
	require.NoError(t, err)
	require.False(t, reserved)
	applied, err := engine.PutOutboxRawIfCurrentGeneration(id, mutationGeneration, []byte("mutation"), nil)
	require.NoError(t, err)
	require.True(t, applied)
}

func TestOutboxNamespaceMigrationPreservesIntentsAndReclaimsReservations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	engine, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)

	intent := downloadIntent{
		File:   protocol.IndexEntry{Name: "pending.txt", Hash: "hash", Version: 1},
		Source: "https://peer",
	}
	rawIntent, err := json.Marshal(intent)
	require.NoError(t, err)
	const legacyGeneration = uint64(41)
	rawGeneration := make([]byte, 8)
	binary.BigEndian.PutUint64(rawGeneration, legacyGeneration)
	require.NoError(t, engine.subscriptions.Update(func(tx *bbolt.Tx) error {
		legacy := tx.Bucket([]byte(bucketNotifyOutbox))
		if err := legacy.Put([]byte(outboxGenerationPrefix+"old-key"), rawGeneration); err != nil {
			return err
		}
		if err := legacy.Put([]byte(downloadIntentKey(intent.File.Name)), rawIntent); err != nil {
			return err
		}
		reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
		return reservations.Put([]byte("abandoned"), rawGeneration)
	}))
	require.NoError(t, engine.Close())

	reopened, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	require.NoError(t, reopened.subscriptions.View(func(tx *bbolt.Tx) error {
		require.Zero(t, tx.Bucket([]byte(bucketNotifyOutboxV2Reservations)).Stats().KeyN)
		require.Zero(t, tx.Bucket([]byte(bucketNotifyOutbox)).Stats().KeyN)
		require.Equal(t, 1, tx.Bucket([]byte(bucketDownloadIntents)).Stats().KeyN)
		return nil
	}))
	generation, err := reopened.ReserveOutboxGeneration("new-key")
	require.NoError(t, err)
	require.Greater(t, generation, legacyGeneration)
	require.Equal(t, 1, reopened.CountDownloadIntents())
}

func TestTombstonePersistenceFailureReturnsServerError(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	persistErr := errors.New("forced tombstone write failure")
	engine.vfs = &failingUpsertIndex{IndexStore: engine.vfs, err: persistErr}
	notification := protocol.PeerNotification{
		File: protocol.IndexEntry{Name: "deleted.txt", Hash: "hash", Version: 2, Deleted: true},
	}
	body, err := json.Marshal(notification)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, protocol.PathNotify, bytes.NewReader(body))
	w := httptest.NewRecorder()

	engine.HandleNotification(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code,
		"sender must retain its outbox entry when tombstone persistence fails")
}

func TestAcceptedDownloadIntentSurvivesRestartUntilBlobStored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := []byte("durable download content")
	sum := sha256.Sum256(content)
	entry := protocol.IndexEntry{
		Name:    "durable.txt",
		Hash:    hex.EncodeToString(sum[:]),
		Size:    int64(len(content)),
		Version: 1,
	}
	notification := protocol.PeerNotification{File: entry, Source: "https://peer-b"}
	body, err := json.Marshal(notification)
	require.NoError(t, err)

	firstAttempts := 0
	first, err := NewStorageEngine(slog.Default(), dir, nil, func(got protocol.IndexEntry, source string) error {
		firstAttempts++
		require.Equal(t, entry, got)
		require.Equal(t, notification.Source, source)
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, first.SetSubscription(entry.Name, true))

	req := httptest.NewRequest(http.MethodPost, protocol.PathNotify, bytes.NewReader(body))
	w := httptest.NewRecorder()
	first.HandleNotification(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, 1, firstAttempts)
	require.Equal(t, 1, first.CountDownloadIntents())
	outboxCount, err := first.CountOutboxEntries()
	require.NoError(t, err)
	require.Zero(t, outboxCount, "download intents must not appear as peer notification outbox rows")
	require.NoError(t, first.ReconcileDownloadIntents())
	require.Equal(t, 1, firstAttempts,
		"an active in-process job is not duplicated; restart is the recovery boundary before StoreRemoteBlob")
	require.NoError(t, first.Close())

	restartedAttempts := 0
	restarted, err := NewStorageEngine(slog.Default(), dir, nil, func(got protocol.IndexEntry, source string) error {
		restartedAttempts++
		require.Equal(t, entry, got)
		require.Equal(t, notification.Source, source)
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, restarted.ReconcileDownloadIntents())
	require.Equal(t, 1, restartedAttempts, "startup reconciliation must re-enqueue accepted volatile work")
	require.NoError(t, restarted.StoreRemoteBlob(entry, bytes.NewReader(content)))
	require.Zero(t, restarted.CountDownloadIntents())
	require.NoError(t, restarted.Close())

	afterCompletionAttempts := 0
	completed, err := NewStorageEngine(slog.Default(), dir, nil, func(protocol.IndexEntry, string) error {
		afterCompletionAttempts++
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, completed.Close()) })
	require.NoError(t, completed.ReconcileDownloadIntents())
	require.Zero(t, afterCompletionAttempts, "completed durable intent must not replay")
}

func TestManifestContinuesAfterFirstEnqueueFailure(t *testing.T) {
	t.Parallel()
	firstErr := errors.New("first enqueue failed")
	var attempted []string
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(file protocol.IndexEntry, source string) error {
		require.Equal(t, "peer-source", source)
		attempted = append(attempted, file.Name)
		if file.Name == "00-first.txt" {
			return firstErr
		}
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	first := protocol.IndexEntry{Name: "00-first.txt", Hash: contentHash([]byte("first")), Version: 1}
	second := protocol.IndexEntry{Name: "10-second.txt", Hash: contentHash([]byte("second")), Version: 1}
	third := protocol.IndexEntry{Name: "20-third.txt", Hash: contentHash([]byte("third")), Version: 1}
	for _, entry := range []protocol.IndexEntry{first, second, third} {
		require.NoError(t, engine.SetSubscription(entry.Name, true))
	}

	equalWinner := protocol.IndexEntry{
		Name:    "30-equal.txt",
		Hash:    strings.Repeat("f", 64),
		Version: 4,
	}
	require.True(t, mustUpsert(t, engine, equalWinner))
	equalLoser := protocol.IndexEntry{
		Name:    equalWinner.Name,
		Hash:    strings.Repeat("0", 64),
		Version: equalWinner.Version,
	}
	liveBeforeDelete := protocol.IndexEntry{
		Name:    "40-deleted.txt",
		Hash:    strings.Repeat("e", 64),
		Version: 1,
	}
	require.True(t, mustUpsert(t, engine, liveBeforeDelete))
	tombstone := liveBeforeDelete
	tombstone.Version++
	tombstone.Deleted = true

	missing, err := engine.ProcessRemoteManifestFromSource(map[string]protocol.IndexEntry{
		third.Name:      third,
		tombstone.Name:  tombstone,
		first.Name:      first,
		equalLoser.Name: equalLoser,
		second.Name:     second,
	}, "peer-source")
	require.ErrorIs(t, err, firstErr)
	require.Equal(t, []protocol.IndexEntry{first, second, third}, missing)
	require.Equal(t, []string{first.Name, second.Name, third.Name}, attempted)
	require.Equal(t, 3, engine.CountDownloadIntents(), "every missing entry must retain a durable intent")

	for _, entry := range []protocol.IndexEntry{first, second, third} {
		current, exists, readErr := engine.GetFileMetaE(entry.Name)
		require.NoError(t, readErr)
		require.True(t, exists)
		require.Equal(t, entry, current)
	}
	currentEqual, exists, err := engine.GetFileMetaE(equalWinner.Name)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, equalWinner, currentEqual, "equal-version loser must not replace the deterministic winner")
	currentDeleted, exists, err := engine.GetFileMetaE(tombstone.Name)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, tombstone, currentDeleted)
}

func mustUpsert(t *testing.T, engine *StorageEngine, entry protocol.IndexEntry) bool {
	t.Helper()
	updated, err := engine.Upsert(entry)
	require.NoError(t, err)
	return updated
}

func TestLocalVFSNotificationIntentIsPreparedBeforeMetadataMutation(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	prepared := false
	finished := make(chan bool, 1)
	engine.SetMutationNotificationHook(func(entry protocol.IndexEntry) (func(bool) error, error) {
		_, exists, readErr := engine.GetFileMetaE(entry.Name)
		require.NoError(t, readErr)
		require.False(t, exists, "WAL intent must be prepared before metadata commits")
		prepared = true
		return func(committed bool) error {
			finished <- committed
			return nil
		}, nil
	})

	require.NoError(t, engine.SaveLocalFile("wal.txt", bytes.NewReader([]byte("content"))))
	require.True(t, prepared)
	require.True(t, <-finished)
}

func TestFailedVFSMetadataMutationCompensatesPreparedIntent(t *testing.T) {
	t.Parallel()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })
	engine.vfs = &failingUpsertIndex{IndexStore: engine.vfs, err: errors.New("forced VFS failure")}

	finished := make(chan bool, 1)
	engine.SetMutationNotificationHook(func(protocol.IndexEntry) (func(bool) error, error) {
		return func(committed bool) error {
			finished <- committed
			return nil
		}, nil
	})

	require.Error(t, engine.SaveLocalFile("failed-wal.txt", bytes.NewReader([]byte("content"))))
	require.False(t, <-finished, "failed metadata mutation must compensate its prepared intent")
}

func TestStaleDownloadRequestCannotReplaceActiveReservation(t *testing.T) {
	t.Parallel()
	current := protocol.IndexEntry{Name: "active.txt", Hash: "new", Version: 2}
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(protocol.IndexEntry, string) error {
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })
	_, err = engine.UpsertAndSubscribe(current, false)
	require.NoError(t, err)
	require.NoError(t, engine.requestRemoteDownload(current, "peer"))

	stale := current
	stale.Hash = "old"
	stale.Version--
	require.ErrorIs(t, engine.requestRemoteDownload(stale, "peer"), ErrBlobDiscarded)

	key := downloadIntentStorageKey(current.Name)
	engine.downloadMu.Lock()
	active := engine.activeDownloads[key]
	engine.downloadMu.Unlock()
	require.Equal(t, current, active.Intent.File,
		"validation failure must not roll back a newer active reservation")
}

func TestTerminalDownloadFailureReleasesOnlyActiveReservation(t *testing.T) {
	t.Parallel()
	entry := protocol.IndexEntry{Name: "retry.txt", Hash: "hash", Version: 1}
	attempts := 0
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(protocol.IndexEntry, string) error {
		attempts++
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })
	_, err = engine.UpsertAndSubscribe(entry, false)
	require.NoError(t, err)
	require.NoError(t, engine.requestRemoteDownload(entry, "peer"))
	require.Equal(t, 1, attempts)

	engine.ReleaseDownloadAttempt(entry)
	require.Equal(t, 1, engine.CountDownloadIntents(),
		"network failure must retain durable intent")
	require.NoError(t, engine.ReconcileDownloadIntents())
	require.Equal(t, 2, attempts, "released attempt must be retryable without restart")
}

func TestManifestMissingBlobPersistsIntentBeforeEnqueue(t *testing.T) {
	t.Parallel()
	entry := protocol.IndexEntry{Name: "manifest.txt", Hash: "hash", Version: 1}
	enqueued := 0
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(got protocol.IndexEntry, source string) error {
		require.Equal(t, entry, got)
		require.Equal(t, "peer-b", source)
		enqueued++
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })
	require.NoError(t, engine.SetSubscription(entry.Name, true))

	missing, err := engine.ProcessRemoteManifestFromSource(map[string]protocol.IndexEntry{
		entry.Name: entry,
	}, "peer-b")
	require.NoError(t, err)
	require.Equal(t, []protocol.IndexEntry{entry}, missing)
	require.Equal(t, 1, engine.CountDownloadIntents())
	require.Equal(t, 1, enqueued)
}

func TestCorruptDownloadQuarantineSurvivesRestartAndAllowsNewSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	expected := sha256.Sum256([]byte("expected"))
	entry := protocol.IndexEntry{
		Name:    "corrupt.txt",
		Hash:    hex.EncodeToString(expected[:]),
		Version: 1,
	}
	attempts := 0
	engine, err := NewStorageEngine(slog.Default(), dir, nil, func(protocol.IndexEntry, string) error {
		attempts++
		return nil
	})
	require.NoError(t, err)
	_, err = engine.UpsertAndSubscribe(entry, false)
	require.NoError(t, err)
	require.NoError(t, engine.requestRemoteDownload(entry, "peer-corrupt"))
	require.Equal(t, 1, attempts)

	err = engine.StoreRemoteBlob(entry, bytes.NewReader([]byte("corrupt")))
	require.ErrorIs(t, err, ErrBlobIntegrity)
	require.Equal(t, 1, engine.CountDownloadIntents())
	require.NoError(t, engine.QuarantineCorruptDownload(entry))
	require.Zero(t, engine.CountDownloadIntents())
	require.NoError(t, engine.Close())

	restartedAttempts := 0
	restarted, err := NewStorageEngine(slog.Default(), dir, nil, func(_ protocol.IndexEntry, source string) error {
		restartedAttempts++
		require.Equal(t, "peer-new", source)
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	require.NoError(t, restarted.ReconcileDownloadIntents())
	require.Zero(t, restartedAttempts, "quarantined corrupt source must not hot-loop after restart")

	require.NoError(t, restarted.requestRemoteDownload(entry, "peer-new"))
	require.Equal(t, 1, restartedAttempts, "a future source must be allowed to retry")
	require.Equal(t, 1, restarted.CountDownloadIntents())
}

func TestLoadPipelineSchemasDurablyNormalizesLegacyZeroVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	engine, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)
	legacy := protocol.PipelineSchema{ID: "legacy-zero"}
	require.NoError(t, engine.subscriptions.Update(func(tx *bbolt.Tx) error {
		return boltPutJSON(tx, bucketPipelineSchemas, legacy.ID, legacy)
	}))

	loaded, err := engine.LoadPipelineSchemas()
	require.NoError(t, err)
	require.Equal(t, 1, loaded[legacy.ID].Version)
	require.NoError(t, engine.Close())

	restarted, err := NewStorageEngine(slog.Default(), dir, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	loaded, err = restarted.LoadPipelineSchemas()
	require.NoError(t, err)
	require.Equal(t, 1, loaded[legacy.ID].Version)
}
