package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
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
	err := p2p.InitCluster(caPath)
	require.NoError(t, err)
	err = p2p.IssueNodeCertificate(caPath, cfg.StoragePath, cfg.ID)
	require.NoError(t, err)
	caCertFile := filepath.Join(caPath, "ca.crt")
	cfg.CAPath = caCertFile
	nodeCertFile := filepath.Join(cfg.StoragePath, cfg.ID+".crt")
	nodeKeyFile := filepath.Join(cfg.StoragePath, cfg.ID+".key")
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)

	customTransport := &http.Transport{
		TLSClientConfig:   clientTLS,
		DisableKeepAlives: true,
	}

	var finalClient p2p.PeerClient
	if mockClient != nil {
		finalClient = mockClient
	} else {
		httpClient := &http.Client{
			Transport: customTransport,
		}
		finalClient = p2p.NewHTTPPeerClient(httpClient)
	}

	app := server.New(cfg, finalClient)
	ts := httptest.NewUnstartedServer(app.MountHandlers())
	ts.TLS = serverTLS
	ts.StartTLS()

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
