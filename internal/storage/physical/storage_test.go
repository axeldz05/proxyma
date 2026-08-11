package storage_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	storage "proxyma/internal/storage/physical"
	"strings"
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

	gotHash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "SaveBlob must return the content's hash SHA-256")

	fullPath := filepath.Join(baseDir, expectedHash)
	info, err := os.Stat(fullPath)
	require.NoError(t, err, "The file must exist in storage with the hash as its name")
	require.False(t, info.IsDir())
}
func Test03ReadBlobStreamsFromDiskUsingHash(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := "some content!"
	savedHash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	var buf bytes.Buffer
	err = aStorage.ReadBlob(savedHash, &buf)

	require.NoError(t, err)
	require.Equal(t, content, buf.String(), "ReadBlob must stream the exact content")
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

func Test06StorageRecognizesTheSameSavedBlob(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	generatedHash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)

	var got bytes.Buffer
	err = aStorage.ReadBlob(generatedHash, &got)
	require.NoError(t, err)
	require.Equal(t, content, got.Bytes())
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

func Test9SaveBlobReturnsSHA256Hash(t *testing.T) {
	aStorage := storage.NewStorage(t.TempDir())
	content := "Super secret message!"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	gotHash, _, err := aStorage.SaveBlob(bytes.NewReader([]byte(content)))

	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "Hash should be the exact SHA-256 of the file content")
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
