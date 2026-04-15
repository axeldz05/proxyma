package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

func main() {
	port := flag.String("port", "8080", "Node port")
	id := flag.String("id", "node-1", "Unique ID for the node")
	storagePath := flag.String("storage", "./storage", "Path to physical folder of blobs")
	workers := flag.Int("workers", 5, "Limit of concurrent downloads")
	debugMode := flag.Bool("debug", false, "Activate diagnostic logs")
	flag.Parse()

	serverTLS, clientTLS, _ := p2p.GenerateOrLoadTLSConfig(*storagePath, *storagePath, *id)
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

	httpSrv := &http.Server{
		Addr:      fmt.Sprintf(":%s", *port),
		Handler:   app.MountHandlers(),
		TLSConfig: serverTLS,
	}

	fmt.Printf("🚀 Proxyma (Nodo %s) initialized on %s\n", app.Config.ID, app.Config.Address)
	if err := httpSrv.ListenAndServeTLS("", ""); err != nil {
		panic(err)
	}
}

