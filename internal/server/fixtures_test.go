package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func NewServer(t *testing.T, cfg protocol.NodeConfig, mockClient p2p.PeerClient) *TestServer {
	caPath := filepath.Dir(cfg.StoragePath)
	testutil.InitClusterCA(t, caPath)
	node := testutil.IssueNode(t, caPath, cfg.StoragePath, cfg.ID)
	cfg.CAPath = node.CACertPath
	serverTLS, clientTLS := node.ServerTLS, node.ClientTLS

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
