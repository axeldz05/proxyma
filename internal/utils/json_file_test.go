package utils_test

import (
	"os"
	"path/filepath"
	"proxyma/internal/utils"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadWriteJSONFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	type cfg struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	require.NoError(t, utils.WriteJSONFile(path, cfg{Name: "node-a", Count: 3}))

	var got cfg
	require.NoError(t, utils.ReadJSONFile(path, &got))
	require.Equal(t, "node-a", got.Name)
	require.Equal(t, 3, got.Count)
}

func TestReadJSONFileErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var dest map[string]any

	require.Error(t, utils.ReadJSONFile(filepath.Join(dir, "missing.json"), &dest))

	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not-json"), 0o644))
	require.Error(t, utils.ReadJSONFile(bad, &dest))
}
