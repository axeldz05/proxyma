package p2p_test

import (
	"os"
	"testing"

	"proxyma/internal/p2p"

	"github.com/stretchr/testify/require"
)

func TestIssueNodeCertificateRejectsMalformedCAPEMWithoutPanic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt func(t *testing.T, certPath, keyPath string)
	}{
		{
			name: "certificate",
			corrupt: func(t *testing.T, certPath, _ string) {
				t.Helper()
				require.NoError(t, os.WriteFile(certPath, []byte("not PEM"), 0o644))
			},
		},
		{
			name: "private key",
			corrupt: func(t *testing.T, _, keyPath string) {
				t.Helper()
				require.NoError(t, os.WriteFile(keyPath, []byte("not PEM"), 0o600))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			caDir := t.TempDir()
			require.NoError(t, p2p.InitCluster(caDir))
			certPath, keyPath := p2p.CACertPaths(caDir)
			tt.corrupt(t, certPath, keyPath)

			var issueErr error
			require.NotPanics(t, func() {
				issueErr = p2p.IssueNodeCertificate(caDir, t.TempDir(), "new-node")
			})
			require.Error(t, issueErr)
			require.Contains(t, issueErr.Error(), "PEM")
		})
	}
}
