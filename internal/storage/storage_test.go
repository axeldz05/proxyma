package storage_test

import (
	"bytes"
	"io"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")

	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath, nil, 0, 
		func(entry protocol.IndexEntry) {}, 
		func(ie protocol.IndexEntry, s string) error { return nil },
	)

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
		cfg.Logger, cfg.StoragePath, nil, 0, 
		func(entry protocol.IndexEntry) {},
		func(ie protocol.IndexEntry, s string) error { return nil },
	)

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

	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath, nil, 0, 
		func(e protocol.IndexEntry) {}, 
		func(ie protocol.IndexEntry, s string) error { return nil },
	)

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

	engine := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath, nil, 0, 
		func(e protocol.IndexEntry) {}, 
		func(ie protocol.IndexEntry, s string) error { return nil },
	)

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
