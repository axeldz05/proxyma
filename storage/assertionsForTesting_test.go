package storage

import (
	"bytes"
	"testing"
	"github.com/stretchr/testify/require"
)

func assertBlobExists(t *testing.T, aStorage *Storage, hash string) {
	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.True(t, exists)
}

func assertBlobDoesNotExists(t *testing.T, aStorage *Storage, hash string) {
	exists, err := aStorage.BlobExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
}

func noErrorSavingBlob(t *testing.T, aStorage *Storage, content []byte) {
	_, _, err := aStorage.SaveBlob(bytes.NewReader(content))
	require.NoError(t, err)
}
