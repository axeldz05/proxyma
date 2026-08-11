package storage

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"proxyma/internal/protocol"
	physical "proxyma/internal/storage/physical"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestCorruptSnapshotAbortsOrphanGC(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	engine, err := NewStorageEngine(slog.Default(), dir, func(protocol.IndexEntry) {}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })

	require.NoError(t, engine.SaveLocalFile("keep.txt", bytes.NewReader([]byte("durable-content"))))
	meta, ok := engine.GetFileMeta("keep.txt")
	require.True(t, ok)
	require.NotEmpty(t, meta.Hash)

	exists, err := engine.HasPhysicalBlob(meta.Hash)
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, engine.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(vfsIndexBucket))
		require.NotNil(t, b)
		return b.Put([]byte("corrupt-entry"), []byte("{not-json"))
	}))

	_, snapErr := engine.GetVFSSnapshot()
	require.Error(t, snapErr, "corrupt index must surface as Snapshot error")

	err = engine.DeleteLocalFile("keep.txt")
	require.Error(t, err, "orphan GC must abort when Snapshot fails")

	exists, err = engine.HasPhysicalBlob(meta.Hash)
	require.NoError(t, err)
	require.True(t, exists, "live blob must not be deleted when VFS snapshot is corrupt")
}

func TestSaveBlobRenameOntoDirectoryFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := physical.NewStorage(dir)

	content := []byte("stat-error-content")
	hash, _, err := st.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
	require.NoError(t, st.DeleteBlob(hash))

	require.NoError(t, os.Mkdir(filepath.Join(dir, hash), 0o755))
	_, _, err = st.SaveBlob(bytes.NewReader(content))
	require.Error(t, err, "SaveBlob must not succeed when destination cannot be written")
}
