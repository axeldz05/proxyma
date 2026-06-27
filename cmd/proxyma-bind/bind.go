package proxyma_bind

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

type LogRecord struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"` // "INFO", "WARN", "ERROR", "DEBUG"
	Message   string `json:"message"`
}

var (
	logBuffer   []LogRecord
	logBufferMu sync.Mutex
)

type LogWriter struct {
	Stdout io.Writer
}

func (w *LogWriter) Write(p []byte) (n int, err error) {
	n, err = w.Stdout.Write(p)
	line := string(p)
	level := "INFO"
	if strings.Contains(line, "level=ERROR") || strings.Contains(line, "level=error") {
		level = "ERROR"
	} else if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=warn") {
		level = "WARN"
	} else if strings.Contains(line, "level=DEBUG") || strings.Contains(line, "level=debug") {
		level = "DEBUG"
	}

	logBufferMu.Lock()
	logBuffer = append(logBuffer, LogRecord{
		Timestamp: time.Now().Format("15:04:05"),
		Level:     level,
		Message:   strings.TrimSpace(line),
	})
	if len(logBuffer) > 1000 {
		logBuffer = logBuffer[len(logBuffer)-1000:]
	}
	logBufferMu.Unlock()

	return n, err
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
	writer := &LogWriter{Stdout: os.Stdout}
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

type PeerStatus struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Online  bool   `json:"online"`
}

// GetPeersJson returns active peers.
func GetPeersJson() string {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return "[]"
	}

	list := []PeerStatus{}
	for id, addr := range srv.GetPeersCopy() {
		list = append(list, PeerStatus{
			ID:      id,
			Address: addr,
			Online:  srv.IsPeerOnline(id),
		})
	}
	b, _ := json.Marshal(list)
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
	defer srvMutex.Unlock()
	if srv == nil {
		return "[]"
	}

	list := []VFSFileStatus{}
	for _, entry := range srv.Storage.GetVFSSnapshot() {
		if entry.Deleted {
			continue
		}
		hasLocal, _ := srv.Storage.HasPhysicalBlob(entry.Hash)
		isSubscribed := srv.Storage.IsSubscribed(entry.Name)
		sentSpeed, recvSpeed := srv.GetCategoryBandwidth("vfs:" + entry.Hash)

		list = append(list, VFSFileStatus{
			Name:       entry.Name,
			Version:    entry.Version,
			Size:       entry.Size,
			Hash:       entry.Hash,
			Subscribed: isSubscribed,
			HasLocal:   hasLocal,
			Deleted:    entry.Deleted,
			UpSpeed:    sentSpeed,
			DownSpeed:  recvSpeed,
		})
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// SyncVFS triggers VFS synchronization.
func SyncVFS() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return "Node is not running"
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
		return "Node is not running"
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
		return "Node is not running"
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
		return "Node is not running"
	}

	err := s.Storage.DeleteLocalCache(name)
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
		return ""
	}
	return s.Storage.GetLocalBlobPath(hash)
}

// DiscoverServices returns active cluster services.
func DiscoverServices() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return "[]"
	}

	names := make(map[string]bool)
	for _, name := range s.Compute.ListServices() {
		names[name] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for peerID := range s.GetPeersCopy() {
		peerSvc, err := s.DiscoverServices(ctx, peerID)
		if err == nil {
			for _, name := range peerSvc {
				names[name] = true
			}
		}
	}

	list := []string{}
	for name := range names {
		list = append(list, name)
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
		return "{}"
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
		return `{"error": "Node is not running"}`
	}

	addr, schema, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}

	var inputs map[string]any
	if payloadJson != "" {
		_ = json.Unmarshal([]byte(payloadJson), &inputs)
	}

	taskID := fmt.Sprintf("task_kt_%d", time.Now().UnixNano())

	var targetPeerID string
	for pid, paddr := range s.GetPeersCopy() {
		if paddr == addr {
			targetPeerID = pid
			break
		}
	}
	if targetPeerID == "" {
		targetPeerID = addr
	}

	payloadMap := make(map[string]any)
	for k, v := range inputs {
		payloadMap[k] = v
	}

	req := protocol.TaskRequest{
		TaskID:  taskID,
		Service: schema.Name,
		ReplyTo: fmt.Sprintf("%s/services/callback", s.Config.Address),
		Payload: payloadMap,
	}

	err = s.DispatchTask(targetPeerID, req)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}

	var resp protocol.ServiceTaskResponse
	completed := false
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		r, ok := s.Compute.GetTaskResponse(taskID)
		if ok {
			if r.Status == "completed" || r.Status == "failed" {
				resp = r
				completed = true
				break
			}
		}
	}

	if !completed {
		return `{"error": "Task execution timed out"}`
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
		return "error: node not running"
	}

	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	expiration := time.Now().Add(15 * time.Minute)
	s.Config.Logger.Info("Token generated in UI", "expires", expiration)
	s.AddPendingInvite(secretHex, expiration)
	return smartToken
}

// JoinCluster joins an existing cluster, writes configuration, and starts the node.
func JoinCluster(storagePath string, token string, nodeID string, port string) string {
	appStorage = storagePath
	writer := &LogWriter{Stdout: os.Stdout}
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
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	if logBuffer == nil {
		return "[]"
	}
	b, _ := json.Marshal(logBuffer)
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
