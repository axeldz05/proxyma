package proxyma_bind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// GenerateInviteToken creates an invite token valid for 15 minutes.
// Failures are BindErrorJSON. Success peels formatActionResult's BindMessageJSON
// envelope back to a raw token string so Android/CLI callers keep a stable wire shape.
func GenerateInviteToken() string {
	raw := InvokeDomainAction("cluster", "invite", nil)
	if IsBindError(raw) {
		return raw
	}
	// Prefer message envelope; fall back to raw / JSON-encoded token.
	var msgEnv struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &msgEnv); err == nil && msgEnv.Message != "" {
		return msgEnv.Message
	}
	var token string
	if err := json.Unmarshal([]byte(raw), &token); err == nil {
		return token
	}
	return raw
}

// JoinCluster joins an existing cluster, writes configuration, and starts the node.
func JoinCluster(storagePath string, token string, nodeID string, port string) string {
	appStorage = storagePath
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	appLogger = protocol.NewLogger(writer, true)

	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'")
	if token == "" {
		return bindErrorJSON(fmt.Errorf("smart token is required"))
	}

	if nodeID == "" {
		nodeID = utils.GenerateDefaultNodeID()
	}

	// Auto load or generate configuration first
	var cfg protocol.NodeConfig
	if c, err := protocol.LoadConfig(appStorage); err == nil {
		cfg = c
	} else {
		// Default config values
		cfg = protocol.NodeConfig{
			Workers:     4,
			StoragePath: appStorage,
		}
	}

	// Prefer stable node-ID hostname (matches `proxyma init` and Docker Compose DNS).
	// Ephemeral bridge/LAN IPs are added later by AnnouncePresence as secondary addresses.
	localAddr := protocol.HTTPSAddr(nodeID, port)

	logFn := func(msg string, err error) {
		if appLogger != nil {
			if err != nil {
				appLogger.Error(msg, "error", err)
			} else {
				appLogger.Info(msg)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), protocol.RPCTimeoutTaskWait)
	defer cancel()

	caCert, cert, privKeyPEM, successfulAddr, err := p2p.JoinCluster(ctx, token, nodeID, localAddr, logFn)
	if err != nil {
		return bindErrorJSON(fmt.Errorf("join failed: %w", err))
	}

	certsDir := filepath.Join(appStorage, "certs")
	stagingDir := filepath.Join(appStorage, "certs.staging")
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return bindErrorJSON(fmt.Errorf("failed to create staging certs dir: %w", err))
	}

	caPath, _ := p2p.CACertPaths(stagingDir)
	certPath, keyPath := p2p.NodeCertPaths(stagingDir, nodeID)

	if err := p2p.WriteNodePEMs(caPath, certPath, keyPath, []byte(caCert), []byte(cert), privKeyPEM); err != nil {
		_ = os.RemoveAll(stagingDir)
		return bindErrorJSON(fmt.Errorf("failed to write node PEMs: %w", err))
	}

	backupDir := filepath.Join(appStorage, "certs.bak")
	_ = os.RemoveAll(backupDir)
	if _, err := os.Stat(certsDir); err == nil {
		if err := os.Rename(certsDir, backupDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return bindErrorJSON(fmt.Errorf("failed to backup existing certs: %w", err))
		}
	}
	if err := os.Rename(stagingDir, certsDir); err != nil {
		_ = os.Rename(backupDir, certsDir) // best-effort restore
		_ = os.RemoveAll(stagingDir)
		return bindErrorJSON(fmt.Errorf("failed to install new certs: %w", err))
	}
	_ = os.RemoveAll(backupDir)

	// Paths must point at the installed certs dir (not staging).
	caPath, _ = p2p.CACertPaths(certsDir)

	workersCount := cfg.Workers
	if workersCount <= 0 {
		workersCount = 4
	}

	bootstrap := successfulAddr
	if u, err := url.Parse(successfulAddr); err == nil && u.Hostname() == "0.0.0.0" {
		port := u.Port()
		if port == "" {
			port = protocol.DefaultTCPPort
		}
		bootstrap = protocol.HTTPSAddr(nodeID, port)
	}

	newCfg := protocol.NodeConfig{
		ID:            nodeID,
		Address:       localAddr,
		StoragePath:   appStorage,
		Workers:       workersCount,
		CAPath:        caPath,
		BootstrapNode: bootstrap,
	}

	err = protocol.SaveConfig(newCfg)
	if err != nil {
		return bindErrorJSON(fmt.Errorf("failed to save config: %w", err))
	}

	// Stop previous server instance and start newly configured one
	StopNode()
	startErr := StartNode(appStorage, true)
	if startErr != "" {
		return startErr
	}

	go func() {
		time.Sleep(1 * time.Second)
		s := getSrv()
		if s != nil {
			_ = s.ExecuteSync()
			_, _ = s.LocalServiceDiscover()
		}
	}()

	return ""
}
