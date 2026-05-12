package storage_test

import (
	"bytes"
	storage "proxyma/internal/storage/physical"
	"testing"

	"github.com/stretchr/testify/require"
)

func assertBlobExists(t *testing.T, aStorage *storage.Storage, hash string) {
	t.Helper()
	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.True(t, exists)
}

func assertBlobDoesNotExists(t *testing.T, aStorage *storage.Storage, hash string) {
	t.Helper()
	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
}

func noErrorSavingBlob(t *testing.T, aStorage *storage.Storage, content []byte) {
	t.Helper()
	_, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
}
