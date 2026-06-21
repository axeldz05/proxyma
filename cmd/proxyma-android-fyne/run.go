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
	wrappedTransport := &p2p.BandwidthRoundTripper{Base: baseTransport}
	peerClient := p2p.NewHTTPPeerClient(wrappedTransport, cfg.BootstrapNode, appLogger)

	srv = server.New(cfg, peerClient)
	wrappedTransport.Recorder = srv
	srv.LoadLocalServices()

	go func() {
		if err := srv.ListenAndServe(srvTLS); err != nil && cfg.Logger != nil {
			cfg.Logger.Error("Server ListenAndServe failed", "error", err)
		}
	}()

	if cfg.BootstrapNode != "" {
		go func() {
			time.Sleep(2 * time.Second)
			if err := srv.AnnouncePresence(cfg.BootstrapNode); err != nil {
				if cfg.Logger != nil {
					cfg.Logger.Error("AnnouncePresence failed", "error", err)
				}
				return
			}
			go srv.StartRelayPolling(appCtx, cfg.BootstrapNode)
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
