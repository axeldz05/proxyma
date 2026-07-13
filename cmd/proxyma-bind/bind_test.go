package proxyma_bind

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNodeLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// Ensure no node is running initially
	StopNode()
	assert.False(t, IsNodeRunning())
	assert.Empty(t, GetNodeID())
	assert.Empty(t, GetNodeAddress())

	// Start the node in a temporary directory
	errStr := StartNode(tempDir, true)
	assert.Empty(t, errStr, "StartNode should succeed and return empty string")

	assert.True(t, IsNodeRunning())
	assert.NotEmpty(t, GetNodeID())
	assert.Equal(t, "https://127.0.0.1:8080", GetNodeAddress())

	// Test bandwidth stats functions (should be 0 or populated)
	assert.Zero(t, GetUploadSpeed())
	assert.Zero(t, GetDownloadSpeed())
	assert.Zero(t, GetTotalSent())
	assert.Zero(t, GetTotalReceived())

	// Verify storage path helper
	assert.Equal(t, tempDir, GetStoragePath())

	// Wait briefly for goroutines
	time.Sleep(100 * time.Millisecond)

	// Clean stop
	StopNode()
	assert.False(t, IsNodeRunning())
	assert.Empty(t, GetNodeID())
	assert.Empty(t, GetNodeAddress())

	// Cleanup remaining mock structures if any
	_ = os.RemoveAll(tempDir)
}
