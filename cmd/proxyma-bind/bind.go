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
	"strings"
	"sync"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
)

var (
	srv        *server.Server
	srvTLS     *tls.Config
	srvMutex   sync.Mutex
	appStorage string
	appLogger  *slog.Logger
	appCtx     context.Context
	appCancel  context.CancelFunc
)

// SetStoragePath configures the active storage path for the out-of-process CLI fallback.
func SetStoragePath(path string) {
	srvMutex.Lock()
	appStorage = path
	srvMutex.Unlock()
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
			localAddr := fmt.Sprintf("https://127.0.0.1:8080")
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
	srvMutex.Lock()
	defer srvMutex.Unlock()
	return srv != nil
}

// GetNodeID returns the active node's ID, or empty string.
func GetNodeID() string {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return ""
	}
	return srv.Config.ID
}

// GetNodeAddress returns the active node's URL, or empty string.
func GetNodeAddress() string {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return ""
	}
	return srv.Config.Address
}

// Bandwidth stats
func GetUploadSpeed() int64 {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return 0
	}
	upSpeed, _ := srv.GetCurrentBandwidth()
	return int64(upSpeed)
}

func GetDownloadSpeed() int64 {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return 0
	}
	_, downSpeed := srv.GetCurrentBandwidth()
	return int64(downSpeed)
}

func GetTotalSent() int64 {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return 0
	}
	totalSent, _ := srv.GetTotalBandwidth()
	return totalSent
}

func GetTotalReceived() int64 {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return 0
	}
	_, totalRecv := srv.GetTotalBandwidth()
	return totalRecv
}

// GetPeersJson returns active peers.
func GetPeersJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "peers", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list := s.LocalPeersList()
	b, _ := json.Marshal(list)
	return string(b)
}

// GetBandwidthStatsJson returns real-time bandwidth statistics.
func GetBandwidthStatsJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "bandwidth", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	stats := s.LocalBandwidthStats()
	b, _ := json.Marshal(stats)
	return string(b)
}

type VFSFileStatus struct {
	Name       string  `json:"name"`
	Version    int     `json:"version"`
	Size       int64   `json:"size"`
	Hash       string  `json:"hash"`
	Subscribed bool    `json:"subscribed"`
	HasLocal   bool    `json:"hasLocal"`
	Deleted    bool    `json:"deleted"`
	UpSpeed    float64 `json:"upSpeed"`
	DownSpeed  float64 `json:"downSpeed"`
}

// GetVFSFilesJson returns JSON array of VFSFileStatus.
func GetVFSFilesJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()
	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "vfs_list", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list := s.LocalVFSList()
	b, _ := json.Marshal(list)
	return string(b)
}

// SyncVFS triggers VFS synchronization.
func SyncVFS() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "sync", nil)
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.ExecuteSync()
	if err != nil {
		return err.Error()
	}
	return ""
}

// UploadFile uploads a local file to the node's VFS.
func UploadFile(name string, filePath string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_upload", map[string]string{
			"path": filePath,
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Sprintf("failed to open file %s: %v", filePath, err)
	}
	defer f.Close()

	err = s.Storage.SaveLocalFile(name, f)
	if err != nil {
		return err.Error()
	}
	return ""
}

// SetSubscription enables/disables subscription for a VFS file.
func SetSubscription(name string, subscribe bool) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		action := "vfs_subscribe"
		if !subscribe {
			action = "vfs_unsubscribe"
		}
		_, err := sendUnixSocketCommand(appStorage, action, map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	s.Storage.SetSubscription(name, subscribe)
	if subscribe {
		go func() {
			_ = s.ExecuteSync()
		}()
	}
	return ""
}

// DeleteLocalCache deletes the local blob copy of a VFS file.
func DeleteLocalCache(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_purge", map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.Storage.DeleteLocalCache(name)
	if err != nil {
		return err.Error()
	}
	return ""
}

// DeleteFile marks a VFS file as deleted in the registry.
func DeleteFile(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_delete", map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.Storage.DeleteLocalFile(name)
	if err != nil {
		return err.Error()
	}
	return ""
}

// GetLocalBlobPath returns absolute local file path for open operations.
func GetLocalBlobPath(hash string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return filepath.Join(appStorage, "blobs", hash)
	}
	return s.Storage.GetLocalBlobPath(hash)
}

// DiscoverServices returns active cluster services.
func DiscoverServices() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_discover", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list, err := s.LocalServiceDiscover()
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, _ := json.Marshal(list)
	return string(b)
}

type ParameterDetail struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

type ServiceDetail struct {
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	ProviderAddress      string            `json:"providerAddress"`
	RequiredPermissions  []string          `json:"requiredPermissions"`
	Parameters           []ParameterDetail `json:"parameters"`
}

// GetServiceDetails gets metadata for a given service.
func GetServiceDetails(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return `{"error": "Node is not running"}`
	}

	addr, schema, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}

	var reqPermissions []string
	hasImageParam := false
	hasFileParam := false

	var params []ParameterDetail
	for pName, rules := range schema.Parameters {
		lower := strings.ToLower(pName)
		isImg := strings.Contains(lower, "image") || strings.Contains(lower, "img") || strings.Contains(lower, "photo")
		isFil := strings.Contains(lower, "file") || strings.Contains(lower, "path")

		if isImg {
			hasImageParam = true
		}
		if isFil {
			hasFileParam = true
		}

		desc := fmt.Sprintf("Provide a text value for %s.", pName)
		switch rules.Type {
		case "bool":
			desc = fmt.Sprintf("Toggle to enable or disable the %s option.", pName)
		case "int", "float":
			desc = fmt.Sprintf("Enter a numerical value for %s.", pName)
		default:
			if isImg {
				desc = fmt.Sprintf("Provide an image file path or capture a photo for %s.", pName)
			}
		}

		params = append(params, ParameterDetail{
			Name:        pName,
			Type:        rules.Type,
			Required:    rules.Required,
			Description: desc,
		})
	}

	if hasImageParam {
		reqPermissions = append(reqPermissions, "Camera (to take photo for upload)")
		reqPermissions = append(reqPermissions, "Gallery / Storage (to select photo)")
	} else if hasFileParam {
		reqPermissions = append(reqPermissions, "Storage (to read/write local files)")
	}

	detail := ServiceDetail{
		Name:                schema.Name,
		Description:         schema.Description,
		ProviderAddress:     addr,
		RequiredPermissions: reqPermissions,
		Parameters:          params,
	}

	b, _ := json.Marshal(detail)
	return string(b)
}

// RunService runs a task and waits up to 30s.
func RunService(name string, payloadJson string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_run", map[string]string{
			"service": name,
			"payload": payloadJson,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	resp, err := s.LocalServiceRun(name, payloadJson)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// GetTaskStatus queries the status of a specific task.
func GetTaskStatus(taskID string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "service_status", map[string]string{
			"task_id": taskID,
		})
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	resp, ok := s.Compute.GetTaskResponse(taskID)
	if !ok {
		return `{"error": "task not found"}`
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// GenerateInviteToken creates an invite token valid for 15 minutes.
func GenerateInviteToken() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "invite_generate", nil)
		if err != nil {
			return "error: " + err.Error()
		}
		var token string
		if err := json.Unmarshal(data, &token); err != nil {
			return "error: invalid token response: " + err.Error()
		}
		return token
	}

	token, err := s.LocalInviteGenerate(15)
	if err != nil {
		return "error: " + err.Error()
	}
	return token
}

// JoinCluster joins an existing cluster, writes configuration, and starts the node.
func JoinCluster(storagePath string, token string, nodeID string, port string) string {
	appStorage = storagePath
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	appLogger = protocol.NewLogger(writer, true)

	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'")
	if token == "" {
		return "error: smart token is required"
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
		return fmt.Sprintf("error: join failed: %v", err)
	}

	certsDir := filepath.Join(appStorage, "certs")
	_ = os.RemoveAll(certsDir)
	_ = os.MkdirAll(certsDir, 0755)

	caPath := filepath.Join(certsDir, "ca.crt")
	certPath := filepath.Join(certsDir, fmt.Sprintf("%s.crt", nodeID))
	keyPath := filepath.Join(certsDir, fmt.Sprintf("%s.key", nodeID))

	_ = os.WriteFile(caPath, []byte(caCert), 0644)
	_ = os.WriteFile(certPath, []byte(cert), 0644)
	_ = os.WriteFile(keyPath, privKeyPEM, 0600)

	newCfg := protocol.NodeConfig{
		ID:            nodeID,
		Address:       localAddr,
		StoragePath:   appStorage,
		Workers:       cfg.Workers,
		CAPath:        caPath,
		BootstrapNode: strings.Replace(successfulAddr, "0.0.0.0", "node-1", 1),
	}

	err = protocol.SaveConfig(newCfg)
	if err != nil {
		return fmt.Sprintf("error: failed to save config: %v", err)
	}

	// Stop previous server instance and start newly configured one
	StopNode()
	startErr := StartNode(appStorage, true)
	if startErr != "" {
		return fmt.Sprintf("error: start failed: %s", startErr)
	}

	go func() {
		time.Sleep(1 * time.Second)
		srvMutex.Lock()
		s := srv
		srvMutex.Unlock()
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

// ChangeStorageLocation stops node, copies directory, updates configs and restarts node.
func ChangeStorageLocation(newPath string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

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

// GetLogsJson returns JSON logs.
func GetLogsJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "logs", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	protocol.LogBufferMu.Lock()
	defer protocol.LogBufferMu.Unlock()
	if protocol.LogBuffer == nil {
		return "[]"
	}
	b, _ := json.Marshal(protocol.LogBuffer)
	return string(b)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

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
