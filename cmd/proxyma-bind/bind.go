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
	return dispatchUnixLocalOrOffline(action, args, localCall, nil)
}

// dispatchUnixStreamOrLocal runs a streaming action in-process or over unix NDJSON (L2).
// onChunk receives each successful data payload; onError/onComplete follow stream lifecycle.
func dispatchUnixStreamOrLocal(
	action string,
	args map[string]string,
	localStream func(s *server.Server, onChunk func(map[string]any)) error,
	onChunkJSON func(chunkJSON string),
	onError func(errMsg string),
	onComplete func(),
) {
	s := getSrv()
	if s != nil {
		go func() {
			err := localStream(s, func(chunk map[string]any) {
				if onChunkJSON != nil {
					b, _ := json.Marshal(chunk)
					onChunkJSON(string(b))
				}
			})
			if err != nil {
				if onError != nil {
					onError(err.Error())
				}
				return
			}
			if onComplete != nil {
				onComplete()
			}
		}()
		return
	}

	go func() {
		conn, err := DialUnix(appStorage)
		if err != nil {
			if onError != nil {
				onError(err.Error())
			}
			return
		}
		defer func() { _ = conn.Close() }()

		if err := WriteUnixRequest(conn, action, args); err != nil {
			if onError != nil {
				onError(err.Error())
			}
			return
		}

		_ = ScanUnixNDJSON(conn, func(resp protocol.UnixResponse) bool {
			if !resp.Success {
				if onError != nil {
					onError(resp.Error)
				}
				return false
			}
			if onChunkJSON != nil && resp.Data != nil {
				onChunkJSON(string(resp.Data))
			}
			return true
		})
		if onComplete != nil {
			onComplete()
		}
	}()
}

// BindErrorJSON formats an error for bind/CLI consumers (SSOT).
func BindErrorJSON(err error) string {
	if err == nil {
		return `{"error":""}`
	}
	return fmt.Sprintf(`{"error": %q}`, err.Error())
}

// bindErrorJSON is the unexported alias used inside this package.
func bindErrorJSON(err error) string { return BindErrorJSON(err) }

// ParseBindError extracts the error message from a bind JSON error envelope.
func ParseBindError(s string) string {
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) == nil {
		if e, ok := m["error"]; ok && e != "" {
			return e
		}
	}
	return s
}

// IsBindError reports whether s is a non-empty bind error JSON envelope.
func IsBindError(s string) bool {
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil {
		return false
	}
	e, ok := m["error"]
	return ok && e != ""
}

// bindOKJSON marshals a successful payload (or wraps a string message).
func bindOKJSON(res any) string {
	if str, ok := res.(string); ok {
		return str
	}
	b, _ := json.Marshal(res)
	return string(b)
}

// BindMessageJSON returns a success envelope with a message field.
func BindMessageJSON(msg string) string {
	return fmt.Sprintf(`{"message": %q}`, msg)
}

// bindMessageJSON is the unexported alias used inside this package.
func bindMessageJSON(msg string) string { return BindMessageJSON(msg) }

// dispatchUnixLocalOrOffline is L3: in-process localCall, else unix, else optional offline fallback.
func dispatchUnixLocalOrOffline(
	action string,
	args map[string]string,
	localCall func(s *server.Server) (any, error),
	offline func() (any, error),
) string {
	s := getSrv()
	if s != nil {
		res, err := localCall(s)
		if err != nil {
			return bindErrorJSON(err)
		}
		return bindOKJSON(res)
	}
	data, err := sendUnixSocketCommand(appStorage, action, args)
	if err == nil {
		return string(data)
	}
	if offline != nil {
		res, offErr := offline()
		if offErr != nil {
			return bindErrorJSON(offErr)
		}
		return bindOKJSON(res)
	}
	return bindErrorJSON(err)
}

// DialUnix opens a connection to the daemon unix socket (L1).
func DialUnix(storagePath string) (net.Conn, error) {
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		return nil, fmt.Errorf("couldn't load config: %w", err)
	}
	sockPath := protocol.UnixSockPath(cfg.StoragePath)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("daemon is unreachable. Is 'proxyma run' active? Error: %w", err)
	}
	return conn, nil
}

// WriteUnixRequest marshals and writes a UnixRequest (L1).
func WriteUnixRequest(conn net.Conn, action string, args map[string]string) error {
	req := protocol.UnixRequest{Action: action, Args: args}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	_, err = conn.Write(reqBytes)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	return nil
}

// ReadUnixResponse reads one unary UnixResponse from conn (L1).
func ReadUnixResponse(conn net.Conn) (protocol.UnixResponse, error) {
	var resp protocol.UnixResponse
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
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return resp, fmt.Errorf("failed to parse daemon response: %w", err)
	}
	return resp, nil
}

// ScanUnixNDJSON scans line-delimited UnixResponse messages (L1).
func ScanUnixNDJSON(conn net.Conn, onLine func(protocol.UnixResponse) bool) error {
	return utils.ScanNDJSON(conn, func(line []byte) bool {
		var resp protocol.UnixResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return true
		}
		return onLine(resp)
	})
}

func sendUnixSocketCommand(storagePath string, action string, args map[string]string) (json.RawMessage, error) {
	conn, err := DialUnix(storagePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := WriteUnixRequest(conn, action, args); err != nil {
		return nil, err
	}

	resp, err := ReadUnixResponse(conn)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Data, nil
}

// StartNode launches the proxyma node.
// Returns empty string on success, or BindErrorJSON on failure.
func StartNode(storagePath string, debug bool) string {
	srvMutex.Lock()
	defer srvMutex.Unlock()

	if srv != nil {
		return BindErrorJSON(fmt.Errorf("node is already running"))
	}

	appStorage = storagePath
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	appLogger = protocol.NewLogger(writer, debug)

	cfg, err := protocol.LoadConfig(appStorage)
	if err != nil {
		if os.IsNotExist(err) {
			// Auto initialize configuration with default port if not found
			nid := utils.GenerateDefaultNodeID()
			localAddr := "https://127.0.0.1:" + protocol.DefaultTCPPort
			if err := p2p.SetupNewNode(appStorage, nid, localAddr); err != nil {
				return BindErrorJSON(fmt.Errorf("failed to setup initial node: %v", err))
			}
			cfg, err = protocol.LoadConfig(appStorage)
			if err != nil {
				return BindErrorJSON(fmt.Errorf("failed to load initial config: %v", err))
			}
		} else {
			return BindErrorJSON(fmt.Errorf("failed to load config: %v", err))
		}
	}
	cfg.Logger = appLogger

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile, nodeKeyFile := p2p.NodeCertPaths(certsDir, cfg.ID)

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return BindErrorJSON(fmt.Errorf("failed to load mTLS certs: %v", err))
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
		return BindErrorJSON(fmt.Errorf("node is not running"))
	}

	newStorage := filepath.Join(newPath, "proxyma_data")

	StopNode()

	if _, err := os.Stat(appStorage); err == nil {
		err = copyDir(appStorage, newStorage)
		if err != nil {
			// Restart on old storage
			_ = StartNode(appStorage, true)
			return BindErrorJSON(fmt.Errorf("failed to copy data: %v", err))
		}
	}

	oldStorage := appStorage
	appStorage = newStorage

	startErr := StartNode(appStorage, true)
	if startErr != "" {
		appStorage = oldStorage
		_ = StartNode(appStorage, true)
		return BindErrorJSON(fmt.Errorf("failed to start on new storage: %s", ParseBindError(startErr)))
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

// GetUISchemaJSON exports the full single-source-of-truth UI Schema registry as JSON.
func GetUISchemaJSON() string {
	return uischema.GetRegistryJSON()
}

// GetDomainSchemaJson exports metadata for a specific domain as JSON.
func GetDomainSchemaJson(domainName string) string {
	return uischema.GetDomainJSON(domainName)
}
