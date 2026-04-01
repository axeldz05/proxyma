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
)

func main() {
	port := flag.String("port", "8080", "Node port")
	id := flag.String("id", "node-1", "Unique ID for the node")
	storagePath := flag.String("storage", "./storage", "Path to physical folder of blobs")
	secret := flag.String("secret", "default-secret", "Password for the cluster")
	workers := flag.Int("workers", 5, "Limit of concurrent downloads")
	servicesFlag := flag.String("services", "", "Comma separated list of services offered by node, e.g. ocr,llm")
	flag.Parse()

	var services []string
	if *servicesFlag != "" {
		services = strings.Split(*servicesFlag, ",")
	}

	cfg := NodeConfig{
		ID:          *id,
		Address:     fmt.Sprintf("http://localhost:%s", *port),
		StoragePath: *storagePath,
		Secret:      *secret,
		Workers:     *workers,
		Services:    services,
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

	httpClient := &http.Client{} 
	s.peerClient = NewHTTPPeerClient(httpClient, cfg.Secret)

	for i := 0; i < s.config.Workers; i++ {
		go s.downloadWorker()
	}

	mux := s.MountHandlers()

	addr := fmt.Sprintf(":%s", *port)
	fmt.Printf("🚀 Proxyma (Nodo %s) initialized.\n", cfg.ID)
	fmt.Printf("📡 Listening port %s\n", *port)
	fmt.Printf("📁 CAS path: %s\n", cfg.StoragePath)
	
	err := http.ListenAndServe(addr, mux)
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
