package storage_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	storage "proxyma/internal/storage/physical"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test01StorageStartsEmpty(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	exists, err := aStorage.BlobExists("0000000000000000000000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	require.False(t, exists)
}

func Test02SaveBlobWritesToDiskAndReturnsHash(t *testing.T) {
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := "blob blob!"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	gotHash, size, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "SaveBlob must return the content's hash SHA-256")
	require.Equal(t, int64(len(content)), size, "SaveBlob must return the content size")

	fullPath := filepath.Join(baseDir, expectedHash)
	info, err := os.Stat(fullPath)
	require.NoError(t, err, "The file must exist in storage with the hash as its name")
	require.False(t, info.IsDir())
}
func Test03ReadBlobStreamsFromDiskUsingHash(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	savedHash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
	var buf bytes.Buffer
	err = aStorage.ReadBlob(savedHash, &buf)

	require.NoError(t, err)
	require.Equal(t, content, buf.Bytes(), "ReadBlob must stream the exact content")
}

func Test04SaveBlobIsIdempotent(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := "duplicated content"
	hash1, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	hash2, _, err := aStorage.SaveBlob(strings.NewReader(content))

	require.NoError(t, err, "Saving an existing blob should not return an error (Idempotence)")
	require.Equal(t, hash1, hash2, "Hashes must be the same")
}

func TestSaveBlobQuarantinesAndRepairsCorruptExistingCASPath(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	content := []byte("trusted content")
	sum := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(sum[:])
	require.NoError(t, os.WriteFile(
		filepath.Join(baseDir, expectedHash),
		[]byte("tampered bytes"),
		0o644,
	))

	aStorage := storage.NewStorage(baseDir)
	savedHash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, expectedHash, savedHash)
	var got bytes.Buffer
	require.NoError(t, aStorage.ReadBlob(savedHash, &got))
	require.Equal(t, content, got.Bytes())
	_, err = os.Stat(filepath.Join(baseDir, ".corrupt-"+expectedHash))
	require.NoError(t, err)
}

func Test05SavingBlobsAreDiscoverable(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())

	content1 := aFileAcceptedByStorage()
	hash1, _, err := aStorage.SaveBlob(bytes.NewReader(content1))
	require.NoError(t, err)

	content2 := aFileAcceptedByStorage2()
	hash2, _, err := aStorage.SaveBlob(bytes.NewReader(content2))
	require.NoError(t, err)

	assertBlobExists(t, aStorage, hash1)
	assertBlobExists(t, aStorage, hash2)
}


func Test07CanNotReadABlobThatDoesNotExistsInTheStorage(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	generatedHash := hex.EncodeToString(hasher.Sum(nil))

	var buf bytes.Buffer
	got := aStorage.ReadBlob(generatedHash, &buf)
	want := storage.ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test8DoesNotDeleteADifferentBlobThanTheSpecified(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	generatedHash := hex.EncodeToString(hasher.Sum(nil))
	noErrorSavingBlob(t, aStorage, content)

	content2 := aFileAcceptedByStorage2()
	hasher2 := sha256.New()
	hasher2.Write([]byte(content2))
	generatedHash2 := hex.EncodeToString(hasher2.Sum(nil))
	noErrorSavingBlob(t, aStorage, content2)

	require.NoError(t, aStorage.DeleteBlob(generatedHash))
	assertBlobDoesNotExists(t, aStorage, generatedHash)
	assertBlobExists(t, aStorage, generatedHash2)
}


func TestBlobExistsClearsStaleCacheWhenFileMissingOnDisk(t *testing.T) {
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := "stale-cache-blob"
	hash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)

	assertBlobExists(t, aStorage, hash)

	require.NoError(t, os.Remove(filepath.Join(baseDir, hash)))

	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists, "BlobExists must Stat disk and clear stale cache")

	// Second call should stay false without resurrecting the cache entry.
	exists, err = aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestBlobExistsQuarantinesCorruptCachedBlob(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := []byte("trusted cache content")
	hash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, hash), []byte("tampered"), 0o600))

	exists, err := aStorage.BlobExists(hash)
	require.ErrorIs(t, err, storage.ErrBlobCorrupt)
	require.False(t, exists)
	_, err = os.Stat(filepath.Join(baseDir, hash))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(baseDir, ".corrupt-"+hash))
	require.NoError(t, err)
}

func TestReadBlobNeverWritesCorruptBytes(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := []byte("trusted read content")
	hash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, hash), []byte("tampered"), 0o600))

	var output bytes.Buffer
	err = aStorage.ReadBlob(hash, &output)
	require.ErrorIs(t, err, storage.ErrBlobCorrupt)
	require.Empty(t, output.Bytes())
}

func TestDeleteBlobClearsCacheWhenAlreadyMissing(t *testing.T) {
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := "idempotent-delete"
	hash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(baseDir, hash)))
	require.NoError(t, aStorage.DeleteBlob(hash))

	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestReadBlobClearsCacheWhenFileRemovedExternally(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	aStorage := storage.NewStorage(baseDir)
	content := "external-removal-test"
	hash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)

	// First verify it exists and is cached
	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.True(t, exists)

	// Remove file behind storage's back
	require.NoError(t, os.Remove(filepath.Join(baseDir, hash)))

	// ReadBlob should detect missing file and clear cache
	var buf bytes.Buffer
	err = aStorage.ReadBlob(hash, &buf)
	require.ErrorIs(t, err, storage.ErrFileDoesNotExist)

	// Subsequent BlobExists must report false
	exists, err = aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestConcurrentReadBlob(t *testing.T) {
	t.Parallel()
	aStorage := storage.NewStorage(t.TempDir())

	content := []byte("concurrent reader payload test")
	hash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)

	const readers = 10
	var wg sync.WaitGroup
	wg.Add(readers)

	for range readers {
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			err := aStorage.ReadBlob(hash, &buf)
			require.NoError(t, err)
			require.Equal(t, content, buf.Bytes())
		}()
	}

	wg.Wait()
}

func TestConcurrentReadBlobCorrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	aStorage := storage.NewStorage(dir)

	content := []byte("corrupt-payload-target")
	hash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)

	// Corrupt the blob on disk
	blobPath := filepath.Join(dir, hash)
	require.NoError(t, os.WriteFile(blobPath, []byte("tampered-bytes"), 0644))

	const readers = 10
	var wg sync.WaitGroup
	wg.Add(readers)

	for range readers {
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			err := aStorage.ReadBlob(hash, &buf)
			require.Error(t, err)
		}()
	}

	wg.Wait()
}
