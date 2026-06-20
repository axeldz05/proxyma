package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"proxyma/internal/protocol"
	"strings"
	"testing"
)

type TestLogWriter struct {
	T *testing.T
}

func (w TestLogWriter) Write(p []byte) (n int, err error) {
	w.T.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func DefaultConfig(t *testing.T, id string) protocol.NodeConfig {
	writer := TestLogWriter{T: t}
	logger := protocol.NewLogger(writer, true).With("node", id)

	return protocol.NodeConfig{
		ID:          id,
		StoragePath: t.TempDir(),
		Workers:     2,
		Logger:      logger,
	}
}

func CalculateHash(t *testing.T, content string) string {
	t.Helper()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}
