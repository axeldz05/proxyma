package storage

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestEqualVersionEntriesConvergeByDocumentedTotalOrder(t *testing.T) {
	t.Parallel()
	a := protocol.IndexEntry{Name: "same.txt", Version: 7, Hash: "aaa", Size: 1}
	b := protocol.IndexEntry{Name: "same.txt", Version: 7, Hash: "bbb", Size: 1}

	first := newDataIntegrityEngine(t, nil)
	updated, err := first.Upsert(a)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = first.Upsert(b)
	require.NoError(t, err)
	require.True(t, updated)

	second := newDataIntegrityEngine(t, nil)
	updated, err = second.Upsert(b)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = second.Upsert(a)
	require.NoError(t, err)
	require.False(t, updated)

	firstWinner, ok, err := first.GetFileMetaE(a.Name)
	require.NoError(t, err)
	require.True(t, ok)
	secondWinner, ok, err := second.GetFileMetaE(a.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, b, firstWinner)
	require.Equal(t, firstWinner, secondWinner)
}

func TestEqualVersionTombstoneDominatesLiveEntry(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)
	live := protocol.IndexEntry{Name: "deleted.txt", Version: 3, Hash: "zzz"}
	tombstone := protocol.IndexEntry{Name: live.Name, Version: live.Version, Hash: "aaa", Deleted: true}

	updated, err := engine.Upsert(live)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = engine.Upsert(tombstone)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = engine.Upsert(live)
	require.NoError(t, err)
	require.False(t, updated)

	current, ok, err := engine.GetFileMetaE(live.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, tombstone, current)
}

func TestRequestRemoteDownloadRejectsStaleMetadataBeforeDurableIntent(t *testing.T) {
	t.Parallel()
	var callbacks atomic.Int32
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(protocol.IndexEntry, string) error {
		callbacks.Add(1)
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })

	current := protocol.IndexEntry{Name: "atomic.txt", Version: 2, Hash: "winner"}
	_, err = engine.UpsertAndSubscribe(current, false)
	require.NoError(t, err)
	stale := protocol.IndexEntry{Name: current.Name, Version: 1, Hash: "loser"}

	err = engine.requestRemoteDownload(stale, "https://stale")
	require.ErrorIs(t, err, ErrBlobDiscarded)
	require.Zero(t, callbacks.Load())
	count, countErr := engine.CountDownloadIntentsE()
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func TestDownloadCompletionInsideCallbackDoesNotRestoreActiveMarker(t *testing.T) {
	t.Parallel()
	content := []byte("completed synchronously")
	entry := protocol.IndexEntry{
		Name:    "sync-complete.txt",
		Version: 1,
		Hash:    contentHash(content),
		Size:    int64(len(content)),
	}
	var engine *StorageEngine
	var err error
	engine, err = NewStorageEngine(slog.Default(), t.TempDir(), nil, func(got protocol.IndexEntry, _ string) error {
		require.Equal(t, entry, got)
		return engine.StoreRemoteBlob(got, bytes.NewReader(content))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	_, err = engine.UpsertAndSubscribe(entry, false)
	require.NoError(t, err)

	require.NoError(t, engine.requestRemoteDownload(entry, "https://peer"))
	count, err := engine.CountDownloadIntentsE()
	require.NoError(t, err)
	require.Zero(t, count)
	engine.downloadMu.Lock()
	require.Empty(t, engine.activeDownloads)
	engine.downloadMu.Unlock()
}

func TestConcurrentDuplicateDownloadReservesBeforeCallback(t *testing.T) {
	t.Parallel()
	content := []byte("single callback")
	entry := protocol.IndexEntry{
		Name:    "single.txt",
		Version: 1,
		Hash:    contentHash(content),
		Size:    int64(len(content)),
	}
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbacks atomic.Int32
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(protocol.IndexEntry, string) error {
		if callbacks.Add(1) == 1 {
			close(callbackStarted)
		}
		<-releaseCallback
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	_, err = engine.UpsertAndSubscribe(entry, false)
	require.NoError(t, err)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- engine.requestRemoteDownload(entry, "https://peer")
	}()
	<-callbackStarted
	require.NoError(t, engine.requestRemoteDownload(entry, "https://peer"))
	require.Equal(t, int32(1), callbacks.Load())
	close(releaseCallback)
	require.NoError(t, <-firstDone)
	require.NoError(t, engine.StoreRemoteBlob(entry, bytes.NewReader(content)))
}

func TestFailedSupersededBlobGCRemainsDurableAndRetryable(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)
	oldContent := []byte("old durable gc")
	require.NoError(t, engine.SaveLocalFile("gc.txt", bytes.NewReader(oldContent)))
	oldHash := contentHash(oldContent)
	oldPath := engine.GetBlobPath(oldHash)
	require.NoError(t, os.Remove(oldPath))
	require.NoError(t, os.Mkdir(oldPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldPath, "blocker"), []byte("x"), 0o644))

	err := engine.SaveLocalFile("gc.txt", bytes.NewReader([]byte("new content")))
	require.Error(t, err)
	pending, pendingErr := engine.pendingBlobGCCount()
	require.NoError(t, pendingErr)
	require.Equal(t, 1, pending)

	require.NoError(t, os.RemoveAll(oldPath))
	require.NoError(t, engine.SweepPendingBlobGC())
	pending, err = engine.pendingBlobGCCount()
	require.NoError(t, err)
	require.Zero(t, pending)
}

func TestSupersededGCIntentFailureRollsBackMetadata(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)
	original := protocol.IndexEntry{Name: "atomic-gc.txt", Version: 1, Hash: "old"}
	updated, err := engine.Upsert(original)
	require.NoError(t, err)
	require.True(t, updated)
	deleteMetadataBucket(t, engine, bucketPendingBlobGC)

	updated, err = engine.Upsert(protocol.IndexEntry{Name: original.Name, Version: 2, Hash: "new"})
	require.Error(t, err)
	require.False(t, updated)
	current, exists, readErr := engine.GetFileMetaE(original.Name)
	require.NoError(t, readErr)
	require.True(t, exists)
	require.Equal(t, original, current)
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	reader  io.Reader
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return r.reader.Read(p)
}

func TestLargeCASInputDoesNotHoldVFSMutationLock(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)
	reader := &blockingReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		reader:  bytes.NewReader([]byte("large-ish payload")),
	}
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- engine.SaveLocalFile("large.txt", reader)
	}()
	<-reader.started

	lockAvailable := engine.mutationMu.TryLock()
	if lockAvailable {
		engine.mutationMu.Unlock()
	}
	close(reader.release)
	require.NoError(t, <-saveDone)
	require.True(t, lockAvailable, "large CAS I/O must not hold the global VFS mutation lock")
	require.NoError(t, engine.SetSubscription("other.txt", true))
}

func TestOutboxGenerationMetadataIsReclaimed(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)

	for i := range 64 {
		id := filepath.Base(t.TempDir())
		generation, err := engine.ReserveOutboxGeneration(id)
		require.NoError(t, err)
		applied, err := engine.PutOutboxRawIfCurrentGeneration(id, generation, []byte("payload"), nil)
		require.NoError(t, err)
		require.True(t, applied)
		require.NoError(t, engine.DeleteOutboxEntry(id))
		_ = i
	}

	err := engine.subscriptions.View(func(tx *bbolt.Tx) error {
		require.Equal(t, 1, tx.Bucket([]byte(bucketNotifyOutboxV2Generations)).Stats().KeyN)
		require.Zero(t, tx.Bucket([]byte(bucketNotifyOutboxV2Reservations)).Stats().KeyN)
		require.Zero(t, tx.Bucket([]byte(bucketNotifyOutboxV2)).Stats().KeyN)
		return nil
	})
	require.NoError(t, err)
}

func TestPendingBlobGCPropagatesDatabaseErrors(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, nil)
	require.NoError(t, engine.Close())
	require.Error(t, engine.SweepPendingBlobGC())
	_, err := engine.pendingBlobGCCount()
	require.Error(t, err)
}

func TestDownloadCallbackFailureRollsBackOnlyItsReservation(t *testing.T) {
	t.Parallel()
	callbackErr := errors.New("queue full")
	entry := protocol.IndexEntry{Name: "rollback.txt", Version: 1, Hash: "hash"}
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), nil, func(protocol.IndexEntry, string) error {
		return callbackErr
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	_, err = engine.UpsertAndSubscribe(entry, false)
	require.NoError(t, err)

	require.ErrorIs(t, engine.requestRemoteDownload(entry, "https://peer"), callbackErr)
	engine.downloadMu.Lock()
	require.Empty(t, engine.activeDownloads)
	engine.downloadMu.Unlock()
	count, err := engine.CountDownloadIntentsE()
	require.NoError(t, err)
	require.Equal(t, 1, count, "durable intent remains for retry")
}
