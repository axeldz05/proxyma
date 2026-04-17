package storage_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath, nil, 0, func(entry protocol.IndexEntry) {},
	)
	defer engine.Close()
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
	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath, nil, 0, func(entry protocol.IndexEntry) {},
	)
	defer engine.Close()

	fileName := "test13.txt"
	fileContent := []byte("hello from test13!!")
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

func TestSyncStorageDownloadsMissingFiles(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	fileName := "missingFile.txt"
	fileContent := "helloo from test10"
	expectedHash := testutil.CalculateHash(t, fileContent)

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return map[string]protocol.IndexEntry{
				fileName: {Name: fileName, Hash: expectedHash, Version: 1, Size: int64(len(fileContent))},
			}, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(fileContent))), nil
		},
	}

	engine := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, mockClient, 2, func(e protocol.IndexEntry) {})
	defer engine.Close()

	engine.SetSubscription(fileName, true)

	err := engine.SyncStorage(map[string]string{"peer1": "fake-address"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		hasBlob, _ := engine.HasPhysicalBlob(expectedHash)
		return hasBlob
	}, 2*time.Second, 100*time.Millisecond, "Worker should download the file asynchronously")

	meta, exists := engine.GetFileMeta(fileName)
	require.True(t, exists)
	require.Equal(t, expectedHash, meta.Hash)
}

func TestSelectiveSynchronization(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	fileAName := "fileA.txt"
	fileBName := "fileB.txt"
	hashA := testutil.CalculateHash(t, "Content A")
	hashB := testutil.CalculateHash(t, "Content B")

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return map[string]protocol.IndexEntry{
				fileAName: {Name: fileAName, Hash: hashA, Version: 1},
				fileBName: {Name: fileBName, Hash: hashB, Version: 1},
			}, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			if hash == hashA {
				return io.NopCloser(bytes.NewReader([]byte("Content A"))), nil
			}
			return io.NopCloser(bytes.NewReader([]byte("Content B"))), nil
		},
	}

	engine := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, mockClient, 2, func(e protocol.IndexEntry) {})
	defer engine.Close()

	engine.SetSubscription(fileAName, true)

	err := engine.SyncStorage(map[string]string{"peer1": "fake-address"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		hasBlob, _ := engine.HasPhysicalBlob(hashA)
		return hasBlob
	}, 2*time.Second, 100*time.Millisecond)

	time.Sleep(500 * time.Millisecond)
	metaB, existsB := engine.GetFileMeta(fileBName)
	require.True(t, existsB)

	hasBlobB, _ := engine.HasPhysicalBlob(metaB.Hash)
	require.False(t, hasBlobB, "Should NOT download the physical blob of file B because it is not subscribed")
}

func TestWorkerPoolLimitsConcurrency(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")
	cfg.Workers = 2

	contentByHash := make(map[string]string)
	manifest := make(map[string]protocol.IndexEntry)

	for i := range 5 {
		content := fmt.Sprintf("content %d", i)
		hash := testutil.CalculateHash(t, content)
		fileName := fmt.Sprintf("file_%d.txt", i)
		contentByHash[hash] = content
		manifest[fileName] = protocol.IndexEntry{
			Name: fileName, Hash: hash, Version: 1,
		}
	}

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return manifest, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			time.Sleep(1 * time.Second)
			content, ok := contentByHash[hash]
			if !ok {
				return nil, fmt.Errorf("hash not found in mock")
			}
			return io.NopCloser(bytes.NewReader([]byte(content))), nil
		},
	}

	engine := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, mockClient, cfg.Workers, func(e protocol.IndexEntry) {})
	defer engine.Close()

	for i := range 5 {
		engine.SetSubscription(fmt.Sprintf("file_%d.txt", i), true)
	}

	start := time.Now()
	err := engine.SyncStorage(map[string]string{"peer1": "fake-address"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		snapshot := engine.GetVFSSnapshot()
		if len(snapshot) < 5 {
			return false
		}

		for _, v := range snapshot {
			hasBlob, _ := engine.HasPhysicalBlob(v.Hash)
			if !hasBlob {
				return false
			}
		}
		return true
	}, 6*time.Second, 100*time.Millisecond)

	duration := time.Since(start)
	require.GreaterOrEqual(t, duration, 2*time.Second, "Too fast. Worker pool isn't limiting concurrency.")
	require.Less(t, duration, 4*time.Second, "Too slow. System is working sequentially.")
}

func TestNetworkRequestRespectsTimeouts(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")
	fakeHash := "hashfalso123"

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return map[string]protocol.IndexEntry{
				"trampa.txt": {Name: "trampa.txt", Hash: fakeHash, Version: 1},
			}, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return io.NopCloser(bytes.NewReader([]byte("too late"))), nil
			}
		},
	}

	engine := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, mockClient, 2, func(e protocol.IndexEntry) {})
	defer engine.Close()

	engine.SetSubscription("trampa.txt", true)
	err := engine.SyncStorage(map[string]string{"peer": "slow-peer"})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	hasBlob, _ := engine.HasPhysicalBlob(fakeHash)
	require.False(t, hasBlob, "Blob should not be downloaded due to timeout")
}
