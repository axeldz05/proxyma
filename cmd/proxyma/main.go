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
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s [init|issue|run] [flags]\n", os.Args[0])
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		runInit()
	case "issue":
		runIssue()
	case "run":
		runServer()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runInit() {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("path", "./certs", "Directory where the CA will be stored")
	fs.Parse(os.Args[2:])
	
	if err := ensureDir(*path); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	err := p2p.InitCluster(*path)
	if err != nil {
		fmt.Printf("Error initializing cluster: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Cluster initialized successfully at %s\n", *path)
}

func runIssue() {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	caPath := fs.String("ca", "./certs", "CA directory path")
	nodePath := fs.String("node-path", "./certs", "Output directory for the node certificate")
	id := fs.String("id", "", "Unique node identifier (mandatory)")
	fs.Parse(os.Args[2:])

	dir := filepath.Dir(*nodePath)
	if err := ensureDir(dir); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if *id == "" {
		fmt.Println("Error: The -id flag is mandatory")
		os.Exit(1)
	}

	err := p2p.IssueNodeCertificate(*caPath, *nodePath, *id)
	if err != nil {
		fmt.Printf("Error issuing certificate: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Certificate issued for node '%s' at %s\n", *id, *nodePath)
}

func runServer() {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	id := fs.String("id", "node-1", "Unique identifier for this node")
	addr := fs.String("addr", "https://127.0.0.1:8080", "Public address of the node")
	storagePath := fs.String("storage", "./data", "Directory for local file storage")
	workers := fs.Int("workers", 4, "Number of concurrent download workers")
	caFile := fs.String("ca", "./certs/ca.crt", "Path to the cluster CA certificate")
	certFile := fs.String("cert", "", "Path to the node certificate (defaults to ./certs/<id>.crt)")
	keyFile := fs.String("key", "", "Path to the node private key (defaults to ./certs/<id>.key)")
	
	fs.Parse(os.Args[2:])

	if *certFile == "" {
		*certFile = fmt.Sprintf("./certs/%s.crt", *id)
	}
	if *keyFile == "" {
		*keyFile = fmt.Sprintf("./certs/%s.key", *id)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("Starting Proxyma node", "id", *id, "address", *addr)

	if err := ensureDir(*storagePath); err != nil {
		logger.Error("Initialization failed", "error", err)
		os.Exit(1)
	}
	logger.Info("Storage directory verified", "path", *storagePath)

	serverTLS, clientTLS, err := p2p.LoadNodeTLS(*caFile, *certFile, *keyFile)
	if err != nil {
		logger.Error("Failed to initialize TLS", "error", err)
		os.Exit(1)
	}

	cfg := protocol.NodeConfig{
		ID:          *id,
		Address:     *addr,
		StoragePath: *storagePath,
		Workers:     *workers,
		Logger:      logger,
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
	}

	srv := server.New(cfg, httpClient)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(serverTLS); err != nil && err != http.ErrServerClosed {
			logger.Error("Server encountered a critical error", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	logger.Info("Shutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Server shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("Proxyma node stopped successfully. Goodbye!")
}
func ensureDir(path string) error {
	cleanPath := filepath.Clean(path)

	err := os.MkdirAll(cleanPath, 0755)
	if err != nil {
		return fmt.Errorf("could not create directory %s: %w", cleanPath, err)
	}
	
	return nil
}
