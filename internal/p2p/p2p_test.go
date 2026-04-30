package p2p_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMTLSConnectionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	caPath := t.TempDir()
	err := p2p.InitCluster(caPath)
	require.NoError(t, err)
	err = p2p.IssueNodeCertificate(caPath, caPath, "1")
	require.NoError(t, err)
	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(caPath, "1.crt")
	nodeKeyFile := filepath.Join(caPath, "1.key")
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err, "Should not fail while generating certs for the cluster")
	handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("hyper secure connection")); err != nil {
			require.NoError(t, err)
		}
	})

	handler := slog.NewTextHandler(testutil.TestLogWriter{T: t}, &slog.HandlerOptions{Level: slog.LevelDebug})
	testSlog := slog.New(handler).With("node", "Test17-mTLS")
	secureServer := httptest.NewUnstartedServer(handlerFunc)
	secureServer.TLS = serverTLS

	secureServer.Config.ErrorLog = slog.NewLogLogger(testSlog.Handler(), slog.LevelError)
	secureServer.StartTLS()
	defer secureServer.Close()

	t.Run("Client succesfully connects to the server", func(t *testing.T) {
		legitClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLS,
			},
		}

		resp, err := legitClient.Get(secureServer.URL)
		require.NoError(t, err, "The client should be able to connect")
		defer func(){ _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("Reject certificates from an unknown CA", func(t *testing.T) {
		hackerDir := t.TempDir()
		err := p2p.InitCluster(hackerDir)
		require.NoError(t, err)
		err = p2p.IssueNodeCertificate(hackerDir, hackerDir, "hacker-node")
		require.NoError(t, err)
		caCertFile := filepath.Join(hackerDir, "ca.crt")
		nodeCertFile := filepath.Join(hackerDir, "hacker-node.crt")
		nodeKeyFile := filepath.Join(hackerDir, "hacker-node.key")
		_, hackerClientTLS , err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
		require.NoError(t, err)

		hackerClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: hackerClientTLS,
			},
		}

		_, err = hackerClient.Get(secureServer.URL)
		require.Error(t, err, "Should fail because the CA is not the same from what the cluster use")
		require.Contains(t, err.Error(), "bad certificate", "The server should reject unknown origin of certificates")
	})
}
