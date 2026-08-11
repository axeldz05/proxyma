package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type TestServer struct {
	*server.Server
	httpTestSrv *httptest.Server
}

func (ts *TestServer) Client() *http.Client {
	return ts.httpTestSrv.Client()
}

func (ts *TestServer) ExpireInvite(secret string) {
	ts.SetPendingInviteExpiration(secret, time.Now().Add(-1*time.Minute))
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	require.NoError(t, err)
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	require.NoError(t, err)
}

func NewServer(t *testing.T, cfg protocol.NodeConfig, mockClient p2p.PeerClient) *TestServer {
	// Shared CA lives at the per-test parent so helpers that call IssueNode(Dir(StoragePath))
	// still find it. Each node also keeps a private copy under StoragePath/certs so
	// rotation pushes cannot clobber a shared ca.crt mid-flight.
	sharedCA := filepath.Dir(cfg.StoragePath)
	if _, err := os.Stat(filepath.Join(sharedCA, "ca.crt")); err != nil {
		testutil.InitClusterCA(t, sharedCA)
	}
	nodeCerts := filepath.Join(cfg.StoragePath, "certs")
	require.NoError(t, os.MkdirAll(nodeCerts, 0755))

	node := testutil.IssueNode(t, sharedCA, nodeCerts, cfg.ID)
	sharedCACert, sharedCAKey := p2p.CACertPaths(sharedCA)
	localCACert, localCAKey := p2p.CACertPaths(nodeCerts)
	copyFile(t, sharedCACert, localCACert)
	copyFile(t, sharedCAKey, localCAKey)

	cfg.CAPath = localCACert
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(localCACert, node.CertPath, node.KeyPath)
	require.NoError(t, err)

	customTransport := &http.Transport{
		TLSClientConfig:   clientTLS,
		DisableKeepAlives: true,
	}

	var finalClient p2p.PeerClient
	if mockClient != nil {
		finalClient = mockClient
	} else {
		finalClient = p2p.NewHTTPPeerClient(customTransport, "", cfg.Logger)
	}

	app, err := server.New(cfg, finalClient)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(app.MountHandlers())
	ts.TLS = serverTLS
	ts.StartTLS()

	app.SetTLSConfigs(ts.TLS, clientTLS)

	ts.Client().Transport = &http.Transport{
		TLSClientConfig:   clientTLS,
		DisableKeepAlives: true,
	}
	app.SetAddress(ts.URL)

	t.Cleanup(func() {
		ts.CloseClientConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := app.Shutdown(ctx)
		require.NoError(t, err, "Node shutdown should not return an error")
		ts.Close()
	})

	return &TestServer{
		Server:      app,
		httpTestSrv: ts,
	}
}
