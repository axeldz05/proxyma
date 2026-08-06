package main

import (
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPeersListDaemonCmd(t *testing.T) {
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "peers", req.Action)
		return []map[string]any{
			{"id": "peer-a", "online": true},
		}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"peers", "list", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestTelemetryStatsDaemonCmd(t *testing.T) {
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "bandwidth", req.Action)
		return protocol.BandwidthStats{
			UploadSpeed:   10,
			DownloadSpeed: 20,
			TotalSent:     100,
			TotalReceived: 200,
		}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"telemetry", "stats", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestPipelineListDaemonCmd(t *testing.T) {
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		require.Equal(t, "pipeline_list", req.Action)
		return []protocol.PipelineSchema{{ID: "p1"}}, nil
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"service", "list_pipelines", "--storage", tempDir})
	require.NoError(t, rootCmd.Execute())
}

func TestPeersListBindErrorPropagates(t *testing.T) {
	tempDir := t.TempDir()
	require.NoError(t, protocol.SaveConfig(protocol.NodeConfig{ID: "test-node", StoragePath: tempDir}))

	l := startMockUnixSocket(t, tempDir, func(req protocol.UnixRequest) (any, error) {
		return nil, errString("daemon down")
	})
	defer func() { _ = l.Close() }()

	rootCmd.SetArgs([]string{"peers", "list", "--storage", tempDir})
	err := rootCmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "daemon down")
}

type errString string

func (e errString) Error() string { return string(e) }
