package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"log/slog"
)

func main() {
	port := flag.String("port", "8080", "Node port")
	id := flag.String("id", "node-1", "Unique ID for the node")
	storagePath := flag.String("storage", "./storage", "Path to physical folder of blobs")
	workers := flag.Int("workers", 5, "Limit of concurrent downloads")
	debugMode := flag.Bool("debug", false, "Activate diagnostic logs")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *debugMode {
		logLevel = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: logLevel}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts)).With("node", *id)

	cfg := NodeConfig{
		ID:          *id,
		Address:     fmt.Sprintf("http://localhost:%s", *port),
		StoragePath: *storagePath,
		Workers:     *workers,
		Logger: 	 logger,
	}

	if err := os.MkdirAll(cfg.StoragePath, 0755); err != nil {
		log.Fatalf("Fatal error: couldn't make a folder for the storage: %v", err)
	}

	s := &Server{
		config:        cfg,
		Peers:         make(map[string]string),
	}

	serverTLS, clientTLS, err := GenerateOrLoadTLSConfig(cfg.StoragePath, cfg.StoragePath, cfg.ID)
	if err != nil {
		log.Fatalf("Fatal error loading TLS: %v", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
	} 
	s.peerClient = NewHTTPPeerClient(httpClient)
	s.compute = NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.storage = NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers)
	mux := s.MountHandlers()

	addr := fmt.Sprintf(":%s", *port)
	fmt.Printf("🚀 Proxyma (Nodo %s) initialized.\n", cfg.ID)
	fmt.Printf("📡 Listening port %s\n", *port)
	fmt.Printf("📁 CAS path: %s\n", cfg.StoragePath)
	
	server := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: serverTLS,
		ErrorLog:  slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelError),
	}

	err = server.ListenAndServeTLS("", "")
	if err != nil {
		log.Fatalf("The program closed unexpectedly: %v", err)
	}
}

func (s *Server) Close() {
	s.server.Close()
	close(s.storage.downloadQueue)
	close(s.compute.taskQueue)
}

func (s *Server) getPeersCopy() map[string]string{
	peers := make(map[string]string, len(s.Peers))
	for k, v := range s.Peers {
		peers[k] = v
	}
	return peers
}
