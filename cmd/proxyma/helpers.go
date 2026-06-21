package proxyma

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
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
	return utils.GenerateDefaultNodeID()
}

func sendUnixSocketCommand(storagePath string, action string, args map[string]string) (json.RawMessage, error) {
	cfg := loadConfigOrDie(storagePath)
	sockPath := filepath.Join(cfg.StoragePath, "proxyma.sock")

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("daemon is unreachable. Is 'proxyma run' active? Error: %w", err)
	}
	defer func() { _ = conn.Close() }()

	req := protocol.UnixRequest{
		Action: action,
		Args:   args,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	_, err = conn.Write(reqBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	var respBytes []byte
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			respBytes = append(respBytes, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	var resp protocol.UnixResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse daemon response: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}

	return resp.Data, nil
}
