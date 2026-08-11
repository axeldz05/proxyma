package testutil

import (
	"crypto/tls"
	"testing"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
)

// NodeTLS is one node's cryptographic material inside a test cluster. Paths stay
// exposed so a test can re-issue, rotate or corrupt a single step by hand instead
// of being forced through the helper.
type NodeTLS struct {
	ID         string
	CADir      string
	NodeDir    string
	CACertPath string
	CertPath   string
	KeyPath    string
	ServerTLS  *tls.Config
	ClientTLS  *tls.Config
}

// InitClusterCA creates a CA under caDir and returns its cert path (L1).
func InitClusterCA(t *testing.T, caDir string) string {
	t.Helper()
	if err := p2p.InitCluster(caDir); err != nil {
		t.Fatalf("init cluster CA in %s: %v", caDir, err)
	}
	caCertPath, _ := p2p.CACertPaths(caDir)
	return caCertPath
}

// IssueNode issues a certificate for id under an existing CA and loads both TLS
// configs (L2). Use it when the CA already exists (e.g. a running sponsor).
func IssueNode(t *testing.T, caDir, nodeDir, id string) NodeTLS {
	t.Helper()
	if err := p2p.IssueNodeCertificate(caDir, nodeDir, id); err != nil {
		t.Fatalf("issue certificate for %s: %v", id, err)
	}
	caCertPath, _ := p2p.CACertPaths(caDir)
	certPath, keyPath := p2p.NodeCertPaths(nodeDir, id)
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("load TLS for %s: %v", id, err)
	}
	return NodeTLS{
		ID:         id,
		CADir:      caDir,
		NodeDir:    nodeDir,
		CACertPath: caCertPath,
		CertPath:   certPath,
		KeyPath:    keyPath,
		ServerTLS:  serverTLS,
		ClientTLS:  clientTLS,
	}
}

// NewNodeTLS is the single-node case: a fresh CA and node material in one temp dir (L3).
func NewNodeTLS(t *testing.T, id string) NodeTLS {
	t.Helper()
	dir := t.TempDir()
	InitClusterCA(t, dir)
	return IssueNode(t, dir, dir, id)
}

// NewStorageEngine builds an engine on cfg with no-op sync callbacks (L2).
// Tests that assert on the callbacks call storage.NewStorageEngine directly.
func NewStorageEngine(t *testing.T, cfg protocol.NodeConfig) *storage.StorageEngine {
	t.Helper()
	engine, err := storage.NewStorageEngine(
		cfg.Logger, cfg.StoragePath,
		func(protocol.IndexEntry) {},
		func(protocol.IndexEntry, string) error { return nil },
	)
	if err != nil {
		t.Fatalf("storage engine for %q: %v", cfg.ID, err)
	}
	return engine
}
