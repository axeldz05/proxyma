package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
)

func getRunningServer() *server.Server {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	return srv
}

func startNode() error {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	cfg, err := protocol.LoadConfig(appStorage)
	if err != nil {
		return err
	}
	cfg.Logger = appLogger

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
	nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return err
	}

	srvTLS = stls
	baseTransport := &http.Transport{TLSClientConfig: ctls}
	wrappedTransport := &bandwidthRoundTripper{base: baseTransport}
	peerClient := p2p.NewHTTPPeerClient(wrappedTransport, cfg.BootstrapNode, appLogger)

	srv = server.New(cfg, peerClient)
	srv.LoadLocalServices()

	go func() error {
		err = srv.ListenAndServe(srvTLS)
		if err != nil {
			return err
		}
		return nil
	}()

	if cfg.BootstrapNode != "" {
		go func() error {
			time.Sleep(2 * time.Second)
			err = srv.AnnouncePresence(cfg.BootstrapNode)
			if err != nil {
				return err
			}
			go srv.StartRelayPolling(appCtx, cfg.BootstrapNode)
			return nil
		}()
	}

	return nil
}

func stopNode() {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	srv = nil
}

type bandwidthRoundTripper struct {
	base http.RoundTripper
}

func (b *bandwidthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body = &utils.CountingReadCloser{
			ReadCloser: req.Body,
			OnRead: func(n int) {
				if s := getRunningServer(); s != nil {
					s.RecordBytesSent(int64(n), req.URL.RequestURI())
				}
			},
		}
	}

	resp, err := b.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil {
		resp.Body = &utils.CountingReadCloser{
			ReadCloser: resp.Body,
			OnRead: func(n int) {
				if s := getRunningServer(); s != nil {
					s.RecordBytesReceived(int64(n), req.URL.RequestURI())
				}
			},
		}
	}
	return resp, nil
}
