package utils_test

import (
	"encoding/json"
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

type blockedJSON struct {
	started chan struct{}
	release chan struct{}
	value   string
}

func (b blockedJSON) MarshalJSON() ([]byte, error) {
	close(b.started)
	<-b.release
	return json.Marshal(b.value)
}

func TestWriteJSONFileDoesNotExposePartialReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "atomic.json")
	require.NoError(t, utils.WriteJSONFile(path, "before"))

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- utils.WriteJSONFile(path, blockedJSON{
			started: started,
			release: release,
			value:   "after",
		})
	}()

	<-started
	var during string
	readErr := utils.ReadJSONFile(path, &during)
	close(release)
	writeErr := <-done

	require.NoError(t, readErr, "a reader must keep seeing the previous complete document")
	require.Equal(t, "before", during)
	require.NoError(t, writeErr)
	var after string
	require.NoError(t, utils.ReadJSONFile(path, &after))
	require.Equal(t, "after", after)
}

func TestWriteJSONFileFailurePreservesPreviousDocument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.json")
	require.NoError(t, utils.WriteJSONFile(path, map[string]string{"state": "committed"}))

	err := utils.WriteJSONFile(path, map[string]any{"unsupported": make(chan int)})
	require.Error(t, err)

	var got map[string]string
	require.NoError(t, utils.ReadJSONFile(path, &got))
	require.Equal(t, map[string]string{"state": "committed"}, got)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed writes must clean up temporary files")
}
