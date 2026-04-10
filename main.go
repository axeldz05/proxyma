package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"proxyma/storage"
	"strings"
	"sync"
	"log/slog"
)

func main() {
	port := flag.String("port", "8080", "Node port")
	id := flag.String("id", "node-1", "Unique ID for the node")
	storagePath := flag.String("storage", "./storage", "Path to physical folder of blobs")
	workers := flag.Int("workers", 5, "Limit of concurrent downloads")
	servicesFlag := flag.String("services", "", "Comma separated list of services offered by node, e.g. ocr,llm")
	debugMode := flag.Bool("debug", false, "Activate diagnostic logs")
	flag.Parse()

	var services []string
	if *servicesFlag != "" {
		services = strings.Split(*servicesFlag, ",")
	}

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
		Services:    services,
		Logger: logger,
	}

	if err := os.MkdirAll(cfg.StoragePath, 0755); err != nil {
		log.Fatalf("Fatal error: couldn't make a folder for the storage: %v", err)
	}

	s := &Server{
		config:        cfg,
		Peers:         make(map[string]string),
		storage:       *storage.NewStorage(cfg.StoragePath),
		vfs:           NewVFS(),
		downloadQueue: make(chan DownloadJob, 1000),
		subscriptions: &sync.Map{},
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

	for i := 0; i < s.config.Workers; i++ {
		go s.downloadWorker()
	}

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
	close(s.downloadQueue)
}

func (s *Server) getPeersCopy() map[string]string{
	peers := make(map[string]string, len(s.Peers))
	for k, v := range s.Peers {
		peers[k] = v
	}
	return peers
}
