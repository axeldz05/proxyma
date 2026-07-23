package proxyma_bind

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
	"proxyma/shared/uischema"
)

var (
	srv        *server.Server
	srvTLS     *tls.Config
	srvMutex   sync.RWMutex
	appStorage string
	appLogger  *slog.Logger
	appCtx     context.Context
	appCancel  context.CancelFunc
)

func getSrv() *server.Server {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return srv
}

// SetStoragePath configures the active storage path for the out-of-process CLI fallback.
func SetStoragePath(path string) {
	srvMutex.Lock()
	appStorage = path
	srvMutex.Unlock()
}

// GetStoragePath returns the currently configured storage path.
func GetStoragePath() string {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return appStorage
}

func dispatchUnixOrLocal(action string, args map[string]string, localCall func(s *server.Server) (any, error)) string {
	s := getSrv()
	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, action, args)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	res, err := localCall(s)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	if str, ok := res.(string); ok {
		return str
	}
	b, _ := json.Marshal(res)
	return string(b)
}

func sendUnixSocketCommand(storagePath string, action string, args map[string]string) (json.RawMessage, error) {
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		return nil, fmt.Errorf("couldn't load config: %w", err)
	}
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

// StartNode launches the proxyma node.
// Returns empty string on success, or the error message on failure.
func StartNode(storagePath string, debug bool) string {
	srvMutex.Lock()
	defer srvMutex.Unlock()

	if srv != nil {
		return "Node is already running"
	}

	appStorage = storagePath
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	appLogger = protocol.NewLogger(writer, debug)

	cfg, err := protocol.LoadConfig(appStorage)
	if err != nil {
		if os.IsNotExist(err) {
			// Auto initialize configuration with default port 8080 if not found
			nid := utils.GenerateDefaultNodeID()
			localAddr := "https://127.0.0.1:8080"
			if err := p2p.SetupNewNode(appStorage, nid, localAddr); err != nil {
				return fmt.Sprintf("failed to setup initial node: %v", err)
			}
			cfg, err = protocol.LoadConfig(appStorage)
			if err != nil {
				return fmt.Sprintf("failed to load initial config: %v", err)
			}
		} else {
			return fmt.Sprintf("failed to load config: %v", err)
		}
	}
	cfg.Logger = appLogger

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
	nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return fmt.Sprintf("failed to load mTLS certs: %v", err)
	}

	srvTLS = stls
	baseTransport := &http.Transport{TLSClientConfig: ctls}
	wrappedTransport := &p2p.BandwidthRoundTripper{Base: baseTransport}
	peerClient := p2p.NewHTTPPeerClient(wrappedTransport, cfg.BootstrapNode, appLogger)

	srv = server.New(cfg, peerClient)
	srv.SetTLSConfigs(stls, ctls)
	wrappedTransport.Recorder = srv
	srv.LoadLocalServices()

	ctx, cancel := context.WithCancel(context.Background())
	appCtx = ctx
	appCancel = cancel

	go func() {
		if err := srv.ListenAndServe(srvTLS); err != nil && appLogger != nil {
			appLogger.Error("Server ListenAndServe failed", "error", err)
		}
	}()

	if cfg.BootstrapNode != "" {
		go func() {
			time.Sleep(2 * time.Second)
			if err := srv.AnnouncePresence(cfg.BootstrapNode); err != nil {
				if appLogger != nil {
					appLogger.Error("AnnouncePresence failed", "error", err)
				}
				return
			}
			go srv.StartRelayPolling(appCtx, cfg.BootstrapNode)
		}()
	}

	return ""
}

// StopNode stops the proxyma node if it is running.
func StopNode() {
	srvMutex.Lock()
	defer srvMutex.Unlock()

	if srv == nil {
		return
	}

	if appCancel != nil {
		appCancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	srv = nil
}

// IsNodeRunning returns true if the server is instantiated.
func IsNodeRunning() bool {
	return getSrv() != nil
}

// GetNodeID returns the active node's ID, or empty string.
func GetNodeID() string {
	s := getSrv()
	if s == nil {
		return ""
	}
	return s.Config.ID
}

// GetNodeAddress returns the active node's URL, or empty string.
func GetNodeAddress() string {
	s := getSrv()
	if s == nil {
		return ""
	}
	return s.Config.Address
}

// Bandwidth stats
func GetUploadSpeed() int64 {
	s := getSrv()
	if s == nil {
		return 0
	}
	upSpeed, _ := s.GetCurrentBandwidth()
	return int64(upSpeed)
}

func GetDownloadSpeed() int64 {
	s := getSrv()
	if s == nil {
		return 0
	}
	_, downSpeed := s.GetCurrentBandwidth()
	return int64(downSpeed)
}

func GetTotalSent() int64 {
	s := getSrv()
	if s == nil {
		return 0
	}
	totalSent, _ := s.GetTotalBandwidth()
	return totalSent
}

func GetTotalReceived() int64 {
	s := getSrv()
	if s == nil {
		return 0
	}
	_, totalRecv := s.GetTotalBandwidth()
	return totalRecv
}

// ChangeStorageLocation stops node, copies directory, updates configs and restarts node.
func ChangeStorageLocation(newPath string) string {
	s := getSrv()
	if s == nil {
		return "Node is not running"
	}

	newStorage := filepath.Join(newPath, "proxyma_data")

	StopNode()

	if _, err := os.Stat(appStorage); err == nil {
		err = copyDir(appStorage, newStorage)
		if err != nil {
			// Restart on old storage
			_ = StartNode(appStorage, true)
			return fmt.Sprintf("failed to copy data: %v", err)
		}
	}

	oldStorage := appStorage
	appStorage = newStorage

	startErr := StartNode(appStorage, true)
	if startErr != "" {
		// Revert to old storage
		appStorage = oldStorage
		_ = StartNode(appStorage, true)
		return fmt.Sprintf("failed to start on new storage: %s", startErr)
	}

	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src string, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	err = os.MkdirAll(dst, srcInfo.Mode())
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			err = copyDir(srcPath, dstPath)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(srcPath, dstPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// GetUISchemaJson exports the full single-source-of-truth UI Schema registry as JSON.
func GetUISchemaJson() string {
	return uischema.GetRegistryJSON()
}

// GetDomainSchemaJson exports metadata for a specific domain as JSON.
func GetDomainSchemaJson(domainName string) string {
	return uischema.GetDomainJSON(domainName)
}

// GetUISchemaJSON serializes the global UI schema domains to JSON.
func GetUISchemaJSON() string {
	b, err := json.Marshal(uischema.Registry)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(b)
}
