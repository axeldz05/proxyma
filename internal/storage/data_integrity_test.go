package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"testing"
	"time"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func newDataIntegrityEngine(t *testing.T, notify func(protocol.IndexEntry)) *StorageEngine {
	t.Helper()
	engine, err := NewStorageEngine(slog.Default(), t.TempDir(), notify, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func deleteMetadataBucket(t *testing.T, engine *StorageEngine, bucket string) {
	t.Helper()
	require.NoError(t, engine.subscriptions.Update(func(tx *bbolt.Tx) error {
		return tx.DeleteBucket([]byte(bucket))
	}))
}

func TestSaveLocalFileRollsBackMetadataAndCompensatesCAS(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, func(protocol.IndexEntry) {})

	oldContent := []byte("committed content")
	newContent := []byte("must be compensated")
	require.NoError(t, engine.SaveLocalFile("report.txt", bytes.NewReader(oldContent)))
	oldMeta, exists, err := engine.GetFileMetaE("report.txt")
	require.NoError(t, err)
	require.True(t, exists)

	deleteMetadataBucket(t, engine, bucketSubscriptions)
	err = engine.SaveLocalFile("report.txt", bytes.NewReader(newContent))
	require.Error(t, err)

	current, exists, err := engine.GetFileMetaE("report.txt")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, oldMeta, current, "metadata write must roll back when subscription write fails")

	oldExists, err := engine.HasPhysicalBlob(oldMeta.Hash)
	require.NoError(t, err)
	require.True(t, oldExists, "the committed blob must survive a failed replacement")
	newExists, err := engine.HasPhysicalBlob(contentHash(newContent))
	require.NoError(t, err)
	require.False(t, newExists, "a newly written CAS object must be compensated after metadata failure")
}

func TestSaveLocalFileGCsSupersededHashOnlyWhenOrphan(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, func(protocol.IndexEntry) {})

	shared := []byte("shared old content")
	require.NoError(t, engine.SaveLocalFile("a.txt", bytes.NewReader(shared)))
	require.NoError(t, engine.SaveLocalFile("b.txt", bytes.NewReader(shared)))
	oldHash := contentHash(shared)

	require.NoError(t, engine.SaveLocalFile("a.txt", bytes.NewReader([]byte("a replacement"))))
	exists, err := engine.HasPhysicalBlob(oldHash)
	require.NoError(t, err)
	require.True(t, exists, "a superseded hash referenced by another name is not orphaned")

	require.NoError(t, engine.SaveLocalFile("b.txt", bytes.NewReader([]byte("b replacement"))))
	exists, err = engine.HasPhysicalBlob(oldHash)
	require.NoError(t, err)
	require.False(t, exists, "the final superseded reference must trigger orphan GC")
}

func TestExplicitStaleOrEqualUpsertDoesNotSubscribeOrNotify(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		version int
		hash    string
	}{
		{name: "stale", version: 2, hash: "stale"},
		{name: "equal-loser", version: 3, hash: "aaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notified := make(chan protocol.IndexEntry, 1)
			engine := newDataIntegrityEngine(t, func(entry protocol.IndexEntry) {
				notified <- entry
			})
			current := protocol.IndexEntry{Name: "versioned.txt", Hash: "zzz", Version: 3}
			updated, err := engine.Upsert(current)
			require.NoError(t, err)
			require.True(t, updated)

			stale := protocol.IndexEntry{Name: current.Name, Hash: tc.hash, Version: tc.version}
			got, err := engine.UpsertAndSubscribe(stale, true)
			require.NoError(t, err)
			require.Equal(t, current, got, "the API must return the entry that remains current")

			subscribed, err := engine.IsSubscribedE(current.Name)
			require.NoError(t, err)
			require.False(t, subscribed)
			select {
			case entry := <-notified:
				t.Fatalf("stale/equal payload was notified: %+v", entry)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestStoreRemoteBlobRequiresCurrentNameVersionHashAndLiveState(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, func(protocol.IndexEntry) {})

	currentContent := []byte("current")
	current := protocol.IndexEntry{
		Name:    "remote.txt",
		Hash:    contentHash(currentContent),
		Size:    int64(len(currentContent)),
		Version: 2,
	}
	updated, err := engine.Upsert(current)
	require.NoError(t, err)
	require.True(t, updated)

	for _, tc := range []struct {
		name     string
		fileInfo protocol.IndexEntry
		content  []byte
	}{
		{
			name: "same version different hash",
			fileInfo: protocol.IndexEntry{
				Name: "remote.txt", Hash: contentHash([]byte("divergent")), Version: 2,
			},
			content: []byte("divergent"),
		},
		{
			name: "deleted payload for live entry",
			fileInfo: protocol.IndexEntry{
				Name: "remote.txt", Hash: current.Hash, Version: 2, Deleted: true,
			},
			content: currentContent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := engine.StoreRemoteBlob(tc.fileInfo, bytes.NewReader(tc.content))
			require.ErrorIs(t, err, ErrBlobDiscarded)
		})
	}

	divergentExists, err := engine.HasPhysicalBlob(contentHash([]byte("divergent")))
	require.NoError(t, err)
	require.False(t, divergentExists, "discarded divergent content must be compensated")
}

func TestStorageStrictReadsPropagateClosedDatabaseErrors(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, func(protocol.IndexEntry) {})
	require.NoError(t, engine.SetSubscription("known.txt", true))
	require.NoError(t, engine.Close())

	_, _, err := engine.GetFileMetaE("known.txt")
	require.Error(t, err)
	_, err = engine.IsSubscribedE("known.txt")
	require.Error(t, err)
	_, err = engine.HasServiceSubscriptionsE()
	require.Error(t, err)
	_, err = engine.IsServiceSubscribedE("known-service")
	require.Error(t, err)
	_, err = engine.ProcessRemoteManifestE(map[string]protocol.IndexEntry{
		"known.txt": {Name: "known.txt", Hash: "hash", Version: 1},
	})
	require.Error(t, err)
	_, err = engine.CountDownloadIntentsE()
	require.Error(t, err)
}

func TestProcessRemoteManifestDoesNotReturnDivergentEqualVersion(t *testing.T) {
	t.Parallel()
	engine := newDataIntegrityEngine(t, func(protocol.IndexEntry) {})
	require.NoError(t, engine.SetSubscription("equal.txt", true))
	current := protocol.IndexEntry{Name: "equal.txt", Hash: "zzz", Version: 4}
	updated, err := engine.Upsert(current)
	require.NoError(t, err)
	require.True(t, updated)

	missing, err := engine.ProcessRemoteManifestE(map[string]protocol.IndexEntry{
		"equal.txt": {Name: "equal.txt", Hash: "aaa", Version: 4},
	})
	require.NoError(t, err)
	require.Empty(t, missing, "equal-version divergent metadata must not schedule stale content")
}
