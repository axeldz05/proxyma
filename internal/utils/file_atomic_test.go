package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "secret.key")
	data := []byte("secret-key-data-payload")

	require.NoError(t, WriteFileAtomic(targetPath, data, 0600))

	readData, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, data, readData)

	info, err := os.Stat(targetPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Overwrite atomically
	updatedData := []byte("updated-secret-key-payload")
	require.NoError(t, WriteFileAtomic(targetPath, updatedData, 0600))

	readData, err = os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, updatedData, readData)
}
