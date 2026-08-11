package storage_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	engine := testutil.NewStorageEngine(cfg)

	fileName := "test11.txt"

	content1 := []byte("hello from test11")
	err := engine.SaveLocalFile(fileName, bytes.NewReader(content1))
	require.NoError(t, err)

	content2 := []byte("goodbye from test11")
	err = engine.SaveLocalFile(fileName, bytes.NewReader(content2))
	require.NoError(t, err)

	meta, exists := engine.GetFileMeta(fileName)

	require.True(t, exists, "The system must track the file by its logic name")
	require.Equal(t, 2, meta.Version, "Version of the file should have been incremented to 2")
	require.NotEmpty(t, meta.Hash, "Hash should exist")
}

func TestLocalDeleteCreatesTombstone(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")
	engine := testutil.NewStorageEngine(cfg)

	fileName := "testLocalDeleteCreatesTombstone.txt"
	fileContent := []byte("hello from testLocalDeleteCreatesTombstone!!")
	err := engine.SaveLocalFile(fileName, bytes.NewReader(fileContent))
	require.NoError(t, err)

	metaBefore, _ := engine.GetFileMeta(fileName)
	require.False(t, metaBefore.Deleted, "File should have not been deleted previously")

	err = engine.DeleteLocalFile(fileName)
	require.NoError(t, err)

	metaAfter, exists := engine.GetFileMeta(fileName)
	require.True(t, exists, "The protocol.IndexEntry of the file should still exist after deleting")
	require.True(t, metaAfter.Deleted, "Deleted should be true in the protocol.IndexEntry")
	require.Equal(t, metaBefore.Version+1, metaAfter.Version, "Version should have been incremented")

	existsInDisk, _ := engine.HasPhysicalBlob(metaBefore.Hash)
	require.False(t, existsInDisk, "The physical blob should have been deleted")
}

func TestStorageEngineProcessesManifestAndStoresBlob(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	fileName := "missingFile.txt"
	fileContent := "helloo from test10"
	expectedHash := testutil.CalculateHash(t, fileContent)

	engine := testutil.NewStorageEngine(cfg)

	engine.SetSubscription(fileName, true)

	remoteManifest := map[string]protocol.IndexEntry{
		fileName: {Name: fileName, Hash: expectedHash, Version: 1, Size: int64(len(fileContent))},
	}

	missingFiles := engine.ProcessRemoteManifest(remoteManifest)
	require.Len(t, missingFiles, 1, "Should identify one missing file")
	require.Equal(t, fileName, missingFiles[0].Name)

	fakeHTTPBody := io.NopCloser(bytes.NewReader([]byte(fileContent)))
	err := engine.StoreRemoteBlob(missingFiles[0], fakeHTTPBody)
	require.NoError(t, err)

	hasBlob, _ := engine.HasPhysicalBlob(expectedHash)
	require.True(t, hasBlob, "Physical blob should exist in disk")

	meta, exists := engine.GetFileMeta(fileName)
	require.True(t, exists, "Metadata should be updated in VFS")
	require.Equal(t, expectedHash, meta.Hash)
}

func TestSelectiveSynchronizationEvaluatesManifestCorrectly(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	fileAName := "fileA.txt"
	fileBName := "fileB.txt"
	hashA := testutil.CalculateHash(t, "Content A")
	hashB := testutil.CalculateHash(t, "Content B")

	engine := testutil.NewStorageEngine(cfg)

	engine.SetSubscription(fileAName, true)
	remoteManifest := map[string]protocol.IndexEntry{
		fileAName: {Name: fileAName, Hash: hashA, Version: 1},
		fileBName: {Name: fileBName, Hash: hashB, Version: 1},
	}
	missingFiles := engine.ProcessRemoteManifest(remoteManifest)
	require.Len(t, missingFiles, 1, "Should ONLY return subscribed files for physical download")
	require.Equal(t, fileAName, missingFiles[0].Name)

	metaA, existsA := engine.GetFileMeta(fileAName)
	require.True(t, existsA)
	require.Equal(t, hashA, metaA.Hash)

	metaB, existsB := engine.GetFileMeta(fileBName)
	require.True(t, existsB)
	require.Equal(t, hashB, metaB.Hash)

	hasBlobA, _ := engine.HasPhysicalBlob(hashA)
	require.False(t, hasBlobA)
}

func TestSnapshotReflectsFullIndexState(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-snapshot-1")

	engine := testutil.NewStorageEngine(cfg)

	fileA := "alpha.txt"
	contentA := []byte("content of alpha")
	err := engine.SaveLocalFile(fileA, bytes.NewReader(contentA))
	require.NoError(t, err)

	fileB := "beta.txt"
	contentB := []byte("content of beta")
	err = engine.SaveLocalFile(fileB, bytes.NewReader(contentB))
	require.NoError(t, err)

	fileC := "gamma.txt"
	contentC := []byte("content of gamma")
	err = engine.SaveLocalFile(fileC, bytes.NewReader(contentC))
	require.NoError(t, err)

	contentA2 := []byte("updated content of alpha")
	err = engine.SaveLocalFile(fileA, bytes.NewReader(contentA2))
	require.NoError(t, err)

	err = engine.DeleteLocalFile(fileC)
	require.NoError(t, err)

	snapshot := engine.GetVFSSnapshot()

	require.Len(t, snapshot, 3, "Snapshot must contain all tracked files including tombstones")

	for _, fileName := range []string{fileA, fileB, fileC} {
		snapshotEntry, existsInSnapshot := snapshot[fileName]
		require.True(t, existsInSnapshot, "Snapshot must include entry for %s", fileName)

		metaEntry, existsInMeta := engine.GetFileMeta(fileName)
		require.True(t, existsInMeta, "GetFileMeta must return entry for %s", fileName)

		require.Equal(t, metaEntry.Name, snapshotEntry.Name, "Name mismatch for %s", fileName)
		require.Equal(t, metaEntry.Version, snapshotEntry.Version, "Version mismatch for %s", fileName)
		require.Equal(t, metaEntry.Hash, snapshotEntry.Hash, "Hash mismatch for %s", fileName)
		require.Equal(t, metaEntry.Size, snapshotEntry.Size, "Size mismatch for %s", fileName)
		require.Equal(t, metaEntry.Deleted, snapshotEntry.Deleted, "Deleted flag mismatch for %s", fileName)
	}

	require.Equal(t, 2, snapshot[fileA].Version, "fileA should be at version 2 after one update")
	require.False(t, snapshot[fileA].Deleted, "fileA should NOT be deleted")
	require.Equal(t, testutil.CalculateHash(t, string(contentA2)), snapshot[fileA].Hash,
		"fileA hash must correspond to the latest content")

	require.Equal(t, 1, snapshot[fileB].Version, "fileB should be at version 1")
	require.False(t, snapshot[fileB].Deleted, "fileB should NOT be deleted")
	require.Equal(t, testutil.CalculateHash(t, string(contentB)), snapshot[fileB].Hash,
		"fileB hash must correspond to its original content")

	require.Equal(t, 2, snapshot[fileC].Version, "fileC should be at version 2 after deletion")
	require.True(t, snapshot[fileC].Deleted, "fileC must be marked as deleted (tombstone)")

	for name := range snapshot {
		isKnown := name == fileA || name == fileB || name == fileC
		require.True(t, isKnown, "Snapshot contains unexpected entry: %s", name)
	}
}

func TestVFSUpsertRejectsDecreasingVersion(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-vfs-version")

	engine := testutil.NewStorageEngine(cfg)

	entry := protocol.IndexEntry{Name: "versioned.txt", Hash: "hash-v3", Version: 3, Size: 100}
	updated := engine.Upsert(entry)
	require.True(t, updated, "First insert should succeed")

	olderEntry := protocol.IndexEntry{Name: "versioned.txt", Hash: "hash-v2", Version: 2, Size: 50}
	updated = engine.Upsert(olderEntry)
	require.False(t, updated, "Upsert with lower version should be rejected")

	meta, exists := engine.GetFileMeta("versioned.txt")
	require.True(t, exists)
	require.Equal(t, 3, meta.Version, "Version should remain at 3")
	require.Equal(t, "hash-v3", meta.Hash, "Hash should remain unchanged")
}

func TestStoreRemoteBlobRejectsCorruptedContent(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-integrity")

	engine := testutil.NewStorageEngine(cfg)

	correctContent := "correct content"
	expectedHash := testutil.CalculateHash(t, correctContent)

	fileInfo := protocol.IndexEntry{Name: "integrity.txt", Hash: expectedHash, Version: 1, Size: int64(len(correctContent))}
	engine.Upsert(fileInfo)

	corruptedBody := io.NopCloser(bytes.NewReader([]byte("corrupted content")))
	err := engine.StoreRemoteBlob(fileInfo, corruptedBody)
	require.Error(t, err, "StoreRemoteBlob should return error on hash mismatch")
	require.Contains(t, err.Error(), "hash mismatch")

	hasBlob, _ := engine.HasPhysicalBlob(expectedHash)
	require.False(t, hasBlob, "The expected blob should not exist after corruption")

	corruptedHash := testutil.CalculateHash(t, "corrupted content")
	hasBlobCorrupt, _ := engine.HasPhysicalBlob(corruptedHash)
	require.False(t, hasBlobCorrupt, "The corrupted blob should have been deleted")
}

func TestProcessRemoteManifestSkipsTombstones(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-tombstone-manifest")

	engine := testutil.NewStorageEngine(cfg)

	fileName := "deleted_file.txt"
	engine.SetSubscription(fileName, true)

	remoteManifest := map[string]protocol.IndexEntry{
		fileName: {Name: fileName, Hash: "hash-deleted", Version: 2, Deleted: true},
	}

	missingFiles := engine.ProcessRemoteManifest(remoteManifest)
	require.Empty(t, missingFiles, "Tombstoned entries should NOT be added to missingFiles")

	meta, exists := engine.GetFileMeta(fileName)
	require.True(t, exists, "Metadata should still be updated via Upsert")
	require.True(t, meta.Deleted, "Entry should be marked as deleted")
}

func TestHandleNotificationRespectsSubscription(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "notif-sub-filter")
	var downloadInvoked bool

	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath,
		func(entry protocol.IndexEntry) {},
		func(ie protocol.IndexEntry, s string) error {
			downloadInvoked = true
			return nil
		},
	)

	subscribedFile := "subscribed.txt"
	unsubscribedFile := "unsubscribed.txt"
	engine.SetSubscription(subscribedFile, true)

	tests := []struct {
		name           string
		fileName       string
		expectDownload bool
	}{
		{"ignores unsubscribed file", unsubscribedFile, false},
		{"enqueues download for subscribed file", subscribedFile, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			downloadInvoked = false
			notification := protocol.PeerNotification{
				File:   protocol.IndexEntry{Name: tt.fileName, Hash: "hash-" + tt.fileName, Version: 1},
				Source: "https://peer:8080",
			}
			body, _ := json.Marshal(notification)
			req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			engine.HandleNotification(w, req)

			require.Equal(t, tt.expectDownload, downloadInvoked,
				"download callback invocation mismatch for %s", tt.name)
		})
	}
}

func TestProcessRemoteDeletionCreatesNewTombstone(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-remote-del-new")

	engine := testutil.NewStorageEngine(cfg)

	tombstone := protocol.IndexEntry{
		Name: "never_existed.txt", Hash: "hash-phantom", Version: 1, Deleted: true,
	}

	// Should not panic or error — the file never existed locally
	engine.ProcessRemoteDeletion(tombstone)

	meta, exists := engine.GetFileMeta("never_existed.txt")
	require.True(t, exists, "A tombstone record should have been created")
	require.True(t, meta.Deleted, "The record should be marked as deleted")
	require.Equal(t, 1, meta.Version)
}

func TestConcurrentStorageEngineAccess(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-concurrent")

	engine := testutil.NewStorageEngine(cfg)

	var wg sync.WaitGroup
	const goroutines = 10

	// Writers: SaveLocalFile
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fileName := fmt.Sprintf("concurrent_%d.txt", idx)
			content := fmt.Sprintf("content for file %d", idx)
			_ = engine.SaveLocalFile(fileName, bytes.NewReader([]byte(content)))
		}(i)
	}

	// Readers: GetFileMeta
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fileName := fmt.Sprintf("concurrent_%d.txt", idx)
			engine.GetFileMeta(fileName)
		}(i)
	}

	wg.Wait()

	// After all goroutines finish, delete half the files concurrently
	for i := range goroutines / 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fileName := fmt.Sprintf("concurrent_%d.txt", idx)
			_ = engine.DeleteLocalFile(fileName)
		}(i)
	}

	// Concurrent reads during deletes
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fileName := fmt.Sprintf("concurrent_%d.txt", idx)
			engine.GetFileMeta(fileName)
		}(i)
	}

	wg.Wait()

	// Verify consistent state: all files should exist in VFS
	snapshot := engine.GetVFSSnapshot()
	require.Equal(t, goroutines, len(snapshot), "All files should be tracked in VFS")
}

func TestCASReferenceCountingAndDeduplication(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-cas")
	engine := testutil.NewStorageEngine(cfg)

	// 1. Upload identical content to two different files (Deduplication)
	content := []byte("shared content")
	err := engine.SaveLocalFile("file1.txt", bytes.NewReader(content))
	require.NoError(t, err)

	meta1, exists := engine.GetFileMeta("file1.txt")
	require.True(t, exists)

	err = engine.SaveLocalFile("file2.txt", bytes.NewReader(content))
	require.NoError(t, err)

	meta2, exists := engine.GetFileMeta("file2.txt")
	require.True(t, exists)

	require.Equal(t, meta1.Hash, meta2.Hash, "Identical content must produce identical hash")

	// Verify one physical blob exists
	existsInDisk, _ := engine.HasPhysicalBlob(meta1.Hash)
	require.True(t, existsInDisk)

	// 2. Delete file1.txt (shared CAS hash).
	// Content should still exist on disk because file2.txt still uses it!
	err = engine.DeleteLocalFile("file1.txt")
	require.NoError(t, err)

	existsInDisk, _ = engine.HasPhysicalBlob(meta1.Hash)
	require.True(t, existsInDisk, "Physical blob must NOT be deleted while file2.txt still references it")

	// 3. Delete file2.txt.
	// Since no active files reference the hash anymore, the blob must be purged.
	err = engine.DeleteLocalFile("file2.txt")
	require.NoError(t, err)

	existsInDisk, _ = engine.HasPhysicalBlob(meta1.Hash)
	require.False(t, existsInDisk, "Physical blob must be deleted when references drop to 0")
}

func TestCleanupTempFilesRespectsActiveDownloads(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-temp")
	engine := testutil.NewStorageEngine(cfg)

	// Create a "recent" temp file mimicking an active download
	recentTempFile, err := os.CreateTemp(cfg.StoragePath, "tmp-blob-*")
	require.NoError(t, err)
	_, _ = recentTempFile.Write([]byte("active data"))
	recentTempPath := recentTempFile.Name()
	_ = recentTempFile.Close()

	// Create an "old" temp file mimicking an orphaned/dead download
	oldTempFile, err := os.CreateTemp(cfg.StoragePath, "tmp-blob-*")
	require.NoError(t, err)
	_, _ = oldTempFile.Write([]byte("stale data"))
	oldTempPath := oldTempFile.Name()
	_ = oldTempFile.Close()

	// Backdate the ModTime of the old temp file to 1 hour ago
	oldTime := time.Now().Add(-1 * time.Hour)
	err = os.Chtimes(oldTempPath, oldTime, oldTime)
	require.NoError(t, err)

	// Execute CleanupTempFiles
	engine.CleanupTempFiles()

	// Verify: recent/active temp file remains, old/stale one is cleaned up!
	_, err = os.Stat(recentTempPath)
	require.NoError(t, err, "Active temp file must NOT be deleted by cleanup sweep")

	_, err = os.Stat(oldTempPath)
	require.True(t, os.IsNotExist(err), "Orphaned temp file must be cleaned up")

	// Clean up recent temp file manually
	_ = os.Remove(recentTempPath)
}

func TestDownloadRejectsNonCASHashPaths(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "dl-sanitize")

	engine := testutil.NewStorageEngine(cfg)

	dbPath := filepath.Join(cfg.StoragePath, "metadata.db")
	dbBytes, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NotEmpty(t, dbBytes)

	req := httptest.NewRequest(http.MethodGet, "/download/metadata.db", nil)
	w := httptest.NewRecorder()
	engine.HandleDownload(w, req)

	require.True(t, w.Code == http.StatusBadRequest || w.Code == http.StatusNotFound,
		"expected 400/404, got %d", w.Code)
	require.NotEqual(t, dbBytes, w.Body.Bytes(), "must not leak metadata.db contents")

	content := []byte("cas-blob-payload")
	hash, _, err := engine.SavePhysicalBlob(bytes.NewReader(content))
	require.NoError(t, err)

	okReq := httptest.NewRequest(http.MethodGet, "/download/"+hash, nil)
	okW := httptest.NewRecorder()
	engine.HandleDownload(okW, okReq)
	require.Equal(t, http.StatusOK, okW.Code)
	require.Equal(t, content, okW.Body.Bytes())
}

func TestRemoteTombstoneNotificationGCsPhysicalBlob(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "tombstone-gc-notify")

	engine := testutil.NewStorageEngine(cfg)

	fileName := "orphan-me.txt"
	content := []byte("blob that should be GC'd on remote tombstone")
	require.NoError(t, engine.SaveLocalFile(fileName, bytes.NewReader(content)))

	meta, ok := engine.GetFileMeta(fileName)
	require.True(t, ok)
	hash := meta.Hash
	hasBlob, _ := engine.HasPhysicalBlob(hash)
	require.True(t, hasBlob)

	tombstone := protocol.IndexEntry{
		Name: fileName, Hash: hash, Version: meta.Version + 1, Deleted: true, Size: meta.Size,
	}
	body, err := json.Marshal(protocol.PeerNotification{File: tombstone, Source: "https://peer:8080"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.HandleNotification(w, req)

	require.True(t, w.Code == http.StatusOK || w.Code == http.StatusAccepted)
	got, exists := engine.GetFileMeta(fileName)
	require.True(t, exists)
	require.True(t, got.Deleted)
	hasBlob, _ = engine.HasPhysicalBlob(hash)
	require.False(t, hasBlob, "physical blob must be GC'd after remote tombstone notification")
}

func TestRemoteTombstoneManifestGCsPhysicalBlob(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "tombstone-gc-manifest")

	engine := testutil.NewStorageEngine(cfg)

	fileName := "manifest-orphan.txt"
	content := []byte("blob GC via ProcessRemoteManifest")
	require.NoError(t, engine.SaveLocalFile(fileName, bytes.NewReader(content)))
	meta, ok := engine.GetFileMeta(fileName)
	require.True(t, ok)

	_ = engine.ProcessRemoteManifest(map[string]protocol.IndexEntry{
		fileName: {Name: fileName, Hash: meta.Hash, Version: meta.Version + 1, Deleted: true, Size: meta.Size},
	})

	got, exists := engine.GetFileMeta(fileName)
	require.True(t, exists)
	require.True(t, got.Deleted)
	hasBlob, _ := engine.HasPhysicalBlob(meta.Hash)
	require.False(t, hasBlob, "physical blob must be GC'd after remote tombstone in manifest")
}

func TestStageLocalFileDistinctLogicalNamesForSameBasename(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "stage-distinct-names")
	engine := testutil.NewStorageEngine(cfg)

	dirA := filepath.Join(t.TempDir(), "dirA")
	dirB := filepath.Join(t.TempDir(), "dirB")
	require.NoError(t, os.MkdirAll(dirA, 0o755))
	require.NoError(t, os.MkdirAll(dirB, 0o755))
	pathA := filepath.Join(dirA, "out.pdf")
	pathB := filepath.Join(dirB, "out.pdf")
	require.NoError(t, os.WriteFile(pathA, []byte("content-A"), 0o644))
	require.NoError(t, os.WriteFile(pathB, []byte("content-B"), 0o644))

	hashA, sizeA, err := engine.StageLocalFile(pathA)
	require.NoError(t, err)
	require.NotEmpty(t, hashA)
	require.Equal(t, int64(len("content-A")), sizeA)

	hashB, sizeB, err := engine.StageLocalFile(pathB)
	require.NoError(t, err)
	require.NotEmpty(t, hashB)
	require.Equal(t, int64(len("content-B")), sizeB)
	require.NotEqual(t, hashA, hashB)

	snap := engine.GetVFSSnapshot()
	var names []string
	for name, entry := range snap {
		if entry.Deleted {
			continue
		}
		names = append(names, name)
		switch entry.Hash {
		case hashA:
			require.NotEqual(t, "out.pdf", name, "logical VFS name must not collide on basename alone")
		case hashB:
			require.NotEqual(t, "out.pdf", name, "logical VFS name must not collide on basename alone")
		}
	}
	require.GreaterOrEqual(t, len(names), 2, "both staged blobs must have distinct VFS index entries")

	metaA, okA := engine.GetFileMeta(snapNameForHash(snap, hashA))
	metaB, okB := engine.GetFileMeta(snapNameForHash(snap, hashB))
	require.True(t, okA)
	require.True(t, okB)
	require.NotEqual(t, metaA.Name, metaB.Name)
	require.Equal(t, protocol.VFSURI(hashA), protocol.VFSURI(metaA.Hash))
	require.Equal(t, protocol.VFSURI(hashB), protocol.VFSURI(metaB.Hash))
}

func snapNameForHash(snap map[string]protocol.IndexEntry, hash string) string {
	for name, entry := range snap {
		if entry.Hash == hash && !entry.Deleted {
			return name
		}
	}
	return ""
}

func TestStageAndRewriteRewritesNestedPayloadPaths(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "stage-and-rewrite")
	engine := testutil.NewStorageEngine(cfg)

	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("hello-stage"), 0o644))

	payload := map[string]any{
		"input": localPath,
		"keep":  "vfs://already-there",
		"nested": map[string]any{
			"file": localPath,
		},
	}
	engine.StageAndRewrite(payload, false)

	require.True(t, protocol.IsVFSURI(payload["input"].(string)))
	require.Equal(t, "vfs://already-there", payload["keep"])
	nested := payload["nested"].(map[string]any)
	require.True(t, protocol.IsVFSURI(nested["file"].(string)))
	require.Equal(t, payload["input"], nested["file"])
}
