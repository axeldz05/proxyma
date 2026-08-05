package proxyma_bind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
)

// GenerateInviteToken creates an invite token valid for 15 minutes.
func GenerateInviteToken() string {
	raw := dispatchUnixOrLocal("invite_generate", nil, func(s *server.Server) (any, error) {
		token, _, err := s.LocalInviteGenerate(server.DefaultInviteMinutes)
		return token, err
	})
	if IsBindError(raw) {
		return raw
	}
	// Unix path returns JSON-encoded string; in-process returns plain token.
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

	localIP := "127.0.0.1"
	ips, _ := utils.GetLocalIPs()
	for _, ip := range ips {
		if ip.To4() != nil {
			localIP = ip.String()
			break
		}
	}
	localAddr := fmt.Sprintf("https://%s:%s", localIP, port)

	logFn := func(msg string, err error) {
		if appLogger != nil {
			if err != nil {
				appLogger.Error(msg, "error", err)
			} else {
				appLogger.Info(msg)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	caCert, cert, privKeyPEM, successfulAddr, err := p2p.JoinCluster(ctx, token, nodeID, localAddr, logFn)
	if err != nil {
		return bindErrorJSON(fmt.Errorf("join failed: %w", err))
	}

	certsDir := filepath.Join(appStorage, "certs")
	_ = os.RemoveAll(certsDir)
	_ = os.MkdirAll(certsDir, 0755)

	caPath := filepath.Join(certsDir, "ca.crt")
	certPath, keyPath := p2p.NodeCertPaths(certsDir, nodeID)

	_ = os.WriteFile(caPath, []byte(caCert), 0644)
	_ = os.WriteFile(certPath, []byte(cert), 0644)
	_ = os.WriteFile(keyPath, privKeyPEM, 0600)

	workersCount := cfg.Workers
	if workersCount <= 0 {
		workersCount = 4
	}

	newCfg := protocol.NodeConfig{
		ID:            nodeID,
		Address:       localAddr,
		StoragePath:   appStorage,
		Workers:       workersCount,
		CAPath:        caPath,
		BootstrapNode: strings.Replace(successfulAddr, "0.0.0.0", "node-1", 1),
	}

	err = protocol.SaveConfig(newCfg)
	if err != nil {
		return bindErrorJSON(fmt.Errorf("failed to save config: %w", err))
	}

	// Stop previous server instance and start newly configured one
	StopNode()
	startErr := StartNode(appStorage, true)
	if startErr != "" {
		return bindErrorJSON(fmt.Errorf("start failed: %s", startErr))
	}

	go func() {
		time.Sleep(1 * time.Second)
		s := getSrv()
		if s != nil {
			_ = s.ExecuteSync()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for peerID := range s.GetPeersCopy() {
				_, _ = s.DiscoverServices(ctx, peerID)
			}
		}
	}()

	return ""
}
