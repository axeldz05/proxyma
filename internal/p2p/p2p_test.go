package p2p_test

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/p2p"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMTLSConnectionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	clusterDir := t.TempDir()
	serverTLS, clientTLS, err := p2p.GenerateOrLoadTLSConfig(clusterDir, clusterDir, "legit-node")
	require.NoError(t, err, "Should not fail while generating certs for the cluster")
	handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hyper secure connection"))
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
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("Reject clients without a cert", func(t *testing.T) {
		nakedClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}

		_, err := nakedClient.Get(secureServer.URL)
		require.Error(t, err, "The client should not be able to connect")
		require.Contains(t, err.Error(), "certificate required", "The server must require a certificate")
	})

	t.Run("Reject certificates from an unknown CA", func(t *testing.T) {
		hackerDir := t.TempDir()

		_, hackerClientTLS, err := p2p.GenerateOrLoadTLSConfig(hackerDir, hackerDir, "hacker-node")
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
