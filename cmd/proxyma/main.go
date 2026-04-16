package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"syscall"
	"time"
)

func main() {
	port := flag.String("port", "8080", "Node port")
	id := flag.String("id", "node-1", "Unique ID for the node")
	storagePath := flag.String("storage", "./storage", "Path to physical folder of blobs")
	workers := flag.Int("workers", 5, "Limit of concurrent downloads")
	debugMode := flag.Bool("debug", false, "Activate diagnostic logs")
	flag.Parse()
	
	// TODO: extract tls creation as a function
	caPath := filepath.Dir(*storagePath)
	err := p2p.InitCluster(caPath)
	if err != nil { 
		panic(err)
	}
	err = p2p.IssueNodeCertificate(caPath, *storagePath, *id)
	if err != nil { 
		panic(err)
	}
	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(*storagePath, *id+".crt")
	nodeKeyFile := filepath.Join(*storagePath, *id+".key")
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	if err != nil { 
		panic(err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
	}

	logLevel := slog.LevelInfo
	if *debugMode {
		logLevel = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: logLevel}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts)).With("node", *id)
	cfg := protocol.NodeConfig{
		ID: *id,
		StoragePath: *storagePath,
		Workers: *workers,
		Logger: logger,
	}
	app := server.New(cfg, httpClient)
	app.SetAddress(fmt.Sprintf("https://localhost:%s", *port))

	go func() {
		if err := app.ListenAndServe(serverTLS); err != nil && err != http.ErrServerClosed {
			cfg.Logger.Error("Server crashed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	cfg.Logger.Info("Interrupt signal received. Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := app.Shutdown(ctx); err != nil {
		cfg.Logger.Error("Server forced to shutdown", "error", err)
		os.Exit(1)
	}
	cfg.Logger.Info("Proxyma exited gracefully. Bye!")
}

