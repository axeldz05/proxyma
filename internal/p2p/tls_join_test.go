package p2p_test

import (
	"proxyma/internal/p2p"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateAndSignCSR(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, p2p.InitCluster(dir))

	csrPEM, keyPEM, err := p2p.GenerateNodeCSR("node-x")
	require.NoError(t, err)
	require.Contains(t, string(csrPEM), "CERTIFICATE REQUEST")
	require.Contains(t, string(keyPEM), "EC PRIVATE KEY")

	caCert, caKey := p2p.CACertPaths(dir)
	certPEM, err := p2p.SignCSR(csrPEM, caCert, caKey)
	require.NoError(t, err)
	require.Contains(t, string(certPEM), "CERTIFICATE")
}

func TestSignCSRRejectsMalformedPEM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, p2p.InitCluster(dir))
	caCert, caKey := p2p.CACertPaths(dir)

	_, err := p2p.SignCSR([]byte("not-a-csr"), caCert, caKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CSR")
}

func TestSignCSRRejectsGarbageCertificateRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, p2p.InitCluster(dir))
	caCert, caKey := p2p.CACertPaths(dir)

	garbage := []byte("-----BEGIN CERTIFICATE REQUEST-----\nYWJjZGVm\n-----END CERTIFICATE REQUEST-----\n")
	_, err := p2p.SignCSR(garbage, caCert, caKey)
	require.Error(t, err)
}

func TestCAHashMismatchTokenStillParsesButHashDiffers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, p2p.InitCluster(dir))
	caCert, _ := p2p.CACertPaths(dir)

	token, secret, err := p2p.GenerateSmartToken("https://127.0.0.1:8443", caCert, "sponsor", "")
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	payload, gotSecret, err := p2p.ParseSmartToken(token)
	require.NoError(t, err)
	require.Equal(t, secret, gotSecret)

	caPEM, err := p2p.ReadCAPEM(caCert)
	require.NoError(t, err)
	realHash, err := p2p.CAHashFromPEM(caPEM)
	require.NoError(t, err)
	require.Equal(t, realHash, payload.CAHash)

	// A different CA produces a different hash (join would fail TLS trust).
	otherDir := t.TempDir()
	require.NoError(t, p2p.InitCluster(otherDir))
	otherCA, _ := p2p.CACertPaths(otherDir)
	otherPEM, err := p2p.ReadCAPEM(otherCA)
	require.NoError(t, err)
	otherHash, err := p2p.CAHashFromPEM(otherPEM)
	require.NoError(t, err)
	require.NotEqual(t, realHash, otherHash)
}

func TestIssueNodeCertificateWritesPaths(t *testing.T) {
	t.Parallel()
	caDir := t.TempDir()
	nodeDir := t.TempDir()
	require.NoError(t, p2p.InitCluster(caDir))
	require.NoError(t, p2p.IssueNodeCertificate(caDir, nodeDir, "node-y"))

	certPath, keyPath := p2p.NodeCertPaths(nodeDir, "node-y")
	require.FileExists(t, certPath)
	require.FileExists(t, keyPath)
}
