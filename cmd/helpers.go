package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
)

// getDefaultStorage returns the default storage path from environment or fallback
func getDefaultStorage() string {
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	return defaultStorage
}

// loadConfigOrDie loads the NodeConfig or exits if not found
func loadConfigOrDie(storagePath string) protocol.NodeConfig {
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		fmt.Printf("❌ Error: Couldn't find config.json in %s. Run 'proxyma init' or 'proxyma join' first.\n", storagePath)
		os.Exit(1)
	}
	return cfg
}

// setupLocalAdminClient configures an HTTP client with mTLS for local admin commands
func setupLocalAdminClient(cfg protocol.NodeConfig) *http.Client {
	caCertFile := filepath.Join(filepath.Dir(cfg.CAPath), "ca.crt")
	nodeCertFile := filepath.Join(filepath.Dir(cfg.CAPath), cfg.ID+".crt")
	nodeKeyFile := filepath.Join(filepath.Dir(cfg.CAPath), cfg.ID+".key")

	_, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	if err != nil {
		fmt.Printf("❌ Error loading local certificates: %v\n", err)
		os.Exit(1)
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
		Timeout: 5 * time.Second,
	}
}

// generateDefaultNodeID generates a fallback node ID using hostname and a short random hex
func generateDefaultNodeID() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "node"
	}
	bytes := make([]byte, 2)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%s-0000", hostname)
	}
	return fmt.Sprintf("%s-%s", hostname, hex.EncodeToString(bytes))
}
