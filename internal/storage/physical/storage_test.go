package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/stretchr/testify/require"
)
func aFileAcceptedByStorage() []byte {
	return []byte{1, 2, 3}
}

func aFileAcceptedByStorage2() []byte {
	return []byte{4, 5, 6}
}

func Test01StorageStartsWithNofiles(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	got, err := aStorage.AmountOfBlobs()
	require.NoError(t, err)
	want := 0
	require.Equal(t, want, got)
}

func Test02SaveBlobWritesToDiskAndReturnsHash(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	content := "blob blob!"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	gotHash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "SaveBlob must return the content's hash SHA-256")

	fullPath := filepath.Join(aStorage.baseDir, expectedHash)
	info, err := os.Stat(fullPath)
	require.NoError(t, err, "The file must exist in storage with the hash as its name")
	require.False(t, info.IsDir())
}
func Test03ReadBlobStreamsFromDiskUsingHash(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	content := "some content!"
	savedHash, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	var buf bytes.Buffer
	err = aStorage.ReadBlob(savedHash, &buf)

	require.NoError(t, err)
	require.Equal(t, content, buf.String(), "ReadBlob must stream the exact content")
}

func Test04SaveBlobIsIdempotent(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	content := "duplicated content"
	hash1, _, err := aStorage.SaveBlob(strings.NewReader(content))
	require.NoError(t, err)
	hash2, _, err := aStorage.SaveBlob(strings.NewReader(content))

	require.NoError(t, err, "Saving an existing blob should not return an error (Idempotence)")
	require.Equal(t, hash1, hash2, "Hashes must be the same")
}

func Test05SavingBlobsIncreasesTheAmountOfBlobs(t *testing.T) {
	aStorage := NewStorage(t.TempDir())

	content1 := aFileAcceptedByStorage()
	_, _, err := aStorage.SaveBlob(bytes.NewReader(content1))
	require.NoError(t, err)

	content2 := aFileAcceptedByStorage2()
	_, _, err = aStorage.SaveBlob(bytes.NewReader(content2))
	require.NoError(t, err)

	got, err := aStorage.AmountOfBlobs()
	require.NoError(t, err)
	want := 2
	require.Equal(t, got, want)
}

func Test06StorageRecognizesTheSameSavedBlob(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	generatedHash, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)

	var got bytes.Buffer
	err = aStorage.ReadBlob(generatedHash, &got)
	require.NoError(t, err)
	require.Equal(t, content, got.Bytes())
}

func Test07CanNotReadABlobThatDoesNotExistsInTheStorage(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	content := aFileAcceptedByStorage()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	generatedHash := hex.EncodeToString(hasher.Sum(nil))

	var buf bytes.Buffer
	got := aStorage.ReadBlob(generatedHash, &buf)
	want := ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test8DoesNotDeleteADifferentBlobThanTheSpecified(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
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
	aStorage := NewStorage(t.TempDir())
	content := "Super secret message!"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	gotHash, _, err := aStorage.SaveBlob(bytes.NewReader([]byte(content)))

	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "Hash should be the exact SHA-256 of the file content")
}
