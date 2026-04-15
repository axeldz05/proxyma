package storage_test

import (
	"bytes"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"testing"
	"proxyma/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestVirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-1")
	
	engine := storage.NewStorageEngine(
		cfg.Logger,
		cfg.StoragePath,
		nil,
		0,
		func(entry protocol.IndexEntry) {},
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
		cfg.Logger,
		cfg.StoragePath,
		nil,
		0,
		func(entry protocol.IndexEntry) {},
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


