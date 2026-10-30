package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"

	"github.com/stretchr/testify/require"
)

func TestGenerateDefaultNodeID(t *testing.T) {
	id := generateDefaultNodeID()
	require.NotEmpty(t, id)

	parts := strings.Split(id, "-")
	require.GreaterOrEqual(t, len(parts), 2, "Default node ID must contain at least one hyphen")

	suffix := parts[len(parts)-1]
	require.Len(t, suffix, 4, "Default node ID must end with 4 hex characters")
	_, err := hex.DecodeString(suffix)
	require.NoError(t, err, "Suffix must be valid hex")

	// Ensure successive invocations generate distinct IDs
	distinct := make(map[string]bool)
	for i := 0; i < 10; i++ {
		distinct[generateDefaultNodeID()] = true
	}
	require.Greater(t, len(distinct), 1, "Expected distinct node IDs across repeated calls")
}

func TestRunInit(t *testing.T) {
	t.Run("default node ID and default port", func(t *testing.T) {
		tempDir := t.TempDir()

		err := runInit(tempDir, "", "")
		require.NoError(t, err)

		// Verify config.json was created with proper defaults
		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.NotEmpty(t, cfg.ID)
		require.Equal(t, protocol.HTTPSAddr(cfg.ID, protocol.DefaultTCPPort), cfg.Address)
		require.Equal(t, tempDir, cfg.StoragePath)
		require.Equal(t, 4, cfg.Workers)

		// Verify CA files
		certsDir := filepath.Join(tempDir, "certs")
		caCert, caKey := p2p.CACertPaths(certsDir)
		require.True(t, utils.FileExists(caCert))
		require.True(t, utils.FileExists(caKey))
		require.Equal(t, caCert, cfg.CAPath)

		// Verify Node certificates
		nodeCert, nodeKey := p2p.NodeCertPaths(certsDir, cfg.ID)
		require.True(t, utils.FileExists(nodeCert))
		require.True(t, utils.FileExists(nodeKey))

		// Verify certificates form a valid TLS configuration
		serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCert, nodeCert, nodeKey)
		require.NoError(t, err)
		require.NotNil(t, serverTLS)
		require.NotNil(t, clientTLS)
	})

	t.Run("custom node ID and custom port", func(t *testing.T) {
		tempDir := t.TempDir()
		customID := "test-custom-node"
		customPort := "9050"

		err := runInit(tempDir, customID, customPort)
		require.NoError(t, err)

		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.Equal(t, customID, cfg.ID)
		require.Equal(t, protocol.HTTPSAddr(customID, customPort), cfg.Address)

		certsDir := filepath.Join(tempDir, "certs")
		nodeCert, nodeKey := p2p.NodeCertPaths(certsDir, customID)
		require.True(t, utils.FileExists(nodeCert))
		require.True(t, utils.FileExists(nodeKey))
	})

	t.Run("invalid port returns error", func(t *testing.T) {
		tempDir := t.TempDir()

		err := runInit(tempDir, "node-1", "not-a-port")
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid port")

		err = runInit(tempDir, "node-1", "70000")
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid port")
	})

	t.Run("invalid storage path returns error", func(t *testing.T) {
		tempDir := t.TempDir()
		// Create a regular file where the directory is expected
		blockerFile := filepath.Join(tempDir, "blocker")
		require.NoError(t, os.WriteFile(blockerFile, []byte("file blocker"), 0o644))

		// Trying to use a child of a regular file as a directory must fail
		invalidStorage := filepath.Join(blockerFile, "subpath")
		err := runInit(invalidStorage, "node-fail", "8000")
		require.Error(t, err)
		require.Contains(t, err.Error(), "error initializing node")
	})

	t.Run("empty storage path falls back to getDefaultStorage", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("PROXYMA_STORAGE", tempDir)

		err := runInit("", "env-store-node", "8000")
		require.NoError(t, err)

		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.Equal(t, "env-store-node", cfg.ID)
		require.Equal(t, tempDir, cfg.StoragePath)
	})

	t.Run("whitespace node ID falls back to generated default node ID", func(t *testing.T) {
		tempDir := t.TempDir()

		err := runInit(tempDir, "   ", "8000")
		require.NoError(t, err)

		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.NotEmpty(t, cfg.ID)
		require.NotEqual(t, "   ", cfg.ID)
	})

	t.Run("idempotency and re-init CA preservation", func(t *testing.T) {
		tempDir := t.TempDir()
		nodeID := "node-idempotent"

		// Initial initialization
		err := runInit(tempDir, nodeID, "8000")
		require.NoError(t, err)

		certsDir := filepath.Join(tempDir, "certs")
		caCertPath, caKeyPath := p2p.CACertPaths(certsDir)
		initialCACert, err := os.ReadFile(caCertPath)
		require.NoError(t, err)
		initialCAKey, err := os.ReadFile(caKeyPath)
		require.NoError(t, err)

		// Second initialization on the same storage path
		err = runInit(tempDir, nodeID, "8000")
		require.NoError(t, err)

		// Verify CA certificate and key are preserved byte-for-byte
		reinitCACert, err := os.ReadFile(caCertPath)
		require.NoError(t, err)
		require.Equal(t, initialCACert, reinitCACert, "CA certificate must NOT be regenerated on re-init")

		reinitCAKey, err := os.ReadFile(caKeyPath)
		require.NoError(t, err)
		require.Equal(t, initialCAKey, reinitCAKey, "CA private key must NOT be regenerated on re-init")

		// Verify configuration remains valid
		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.Equal(t, nodeID, cfg.ID)

		// Re-init with a different node ID re-issues node certs and updates config while keeping CA
		err = runInit(tempDir, "node-updated", "9000")
		require.NoError(t, err)

		updatedCACert, err := os.ReadFile(caCertPath)
		require.NoError(t, err)
		require.Equal(t, initialCACert, updatedCACert, "CA certificate must remain intact after node ID change")

		cfgUpdated, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.Equal(t, "node-updated", cfgUpdated.ID)
		require.Equal(t, protocol.HTTPSAddr("node-updated", "9000"), cfgUpdated.Address)

		nodeCertUpdated, _ := p2p.NodeCertPaths(certsDir, "node-updated")
		require.True(t, utils.FileExists(nodeCertUpdated))
	})
}

func TestInitCobraCommand(t *testing.T) {
	t.Run("successful execution via rootCmd with explicit flags", func(t *testing.T) {
		resetRootCommandState(t)
		tempDir := t.TempDir()

		rootCmd.SetArgs([]string{
			"init",
			"--storage", tempDir,
			"--id", "cobra-init-node",
			"--port", "8443",
		})
		require.NoError(t, rootCmd.Execute())

		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.Equal(t, "cobra-init-node", cfg.ID)
		require.Equal(t, protocol.HTTPSAddr("cobra-init-node", "8443"), cfg.Address)
	})

	t.Run("successful execution via rootCmd with default flags", func(t *testing.T) {
		resetRootCommandState(t)
		tempDir := t.TempDir()

		rootCmd.SetArgs([]string{
			"init",
			"--storage", tempDir,
		})
		require.NoError(t, rootCmd.Execute())

		cfg, err := protocol.LoadConfig(tempDir)
		require.NoError(t, err)
		require.NotEmpty(t, cfg.ID)
		require.Equal(t, protocol.HTTPSAddr(cfg.ID, protocol.DefaultTCPPort), cfg.Address)
	})

	t.Run("invalid port via rootCmd returns error", func(t *testing.T) {
		resetRootCommandState(t)
		tempDir := t.TempDir()

		rootCmd.SetArgs([]string{
			"init",
			"--storage", tempDir,
			"--port", "bad-port",
		})
		err := rootCmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid port")
	})
}
