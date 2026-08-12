package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func TestVfsCmds(t *testing.T) {
	resetRootCommandState(t)
	tempDir := t.TempDir()

	cfg := protocol.NodeConfig{
		ID:          "test-node",
		StoragePath: tempDir,
	}
	err := protocol.SaveConfig(cfg)
	require.NoError(t, err)

	t.Run("storage list - empty", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_list", req.Action)
			return []protocol.VFSFileStatus{}, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "list", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage list - entries", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_list", req.Action)
			return []protocol.VFSFileStatus{
				{
					Name:       "file.txt",
					Version:    2,
					Size:       1024,
					Hash:       "abc",
					Deleted:    false,
					Subscribed: true,
					HasLocal:   true,
				},
			}, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "list", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage upload", func(t *testing.T) {
		dummyFile := filepath.Join(tempDir, "upload.txt")
		err := os.WriteFile(dummyFile, []byte("hello"), 0644)
		require.NoError(t, err)

		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_upload", req.Action)
			require.Equal(t, "custom_name.txt", req.Args["name"])
			return nil, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "upload", "--path", dummyFile, "--name", "custom_name.txt", "--storage", tempDir})
		err = rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage subscribe", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_subscribe", req.Action)
			require.Equal(t, "file.txt", req.Args["name"])
			return nil, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "subscribe", "--name", "file.txt", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage unsubscribe", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_unsubscribe", req.Action)
			require.Equal(t, "file.txt", req.Args["name"])
			return nil, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "unsubscribe", "--name", "file.txt", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage delete", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_delete", req.Action)
			require.Equal(t, "file.txt", req.Args["name"])
			return nil, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "delete", "--name", "file.txt", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("storage purge", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			require.Equal(t, "vfs_purge", req.Action)
			require.Equal(t, "file.txt", req.Args["name"])
			return nil, nil
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "purge", "--name", "file.txt", "--storage", tempDir})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	t.Run("daemon unreachable error", func(t *testing.T) {
		rootCmd.SetArgs([]string{"storage", "list", "--storage", tempDir})
		err := rootCmd.Execute()
		require.Error(t, err)
	})

	t.Run("daemon returning error response", func(t *testing.T) {
		l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
			return nil, errors.New("something went wrong")
		})
		defer func() { _ = l.Close() }()

		rootCmd.SetArgs([]string{"storage", "list", "--storage", tempDir})
		err := rootCmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "something went wrong")
	})
}
