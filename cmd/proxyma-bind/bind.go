package proxyma_bind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
	"proxyma/shared/uischema"
)

var (
	srv          *server.Server
	srvMutex     sync.RWMutex
	appStorage   string
	appLogger    *slog.Logger
	appCtx       context.Context
	appCancel    context.CancelFunc
	appWork      *sync.WaitGroup
	appStopping  bool
	appFinalizer *nodeFinalizer

	unixResponseIdleTimeout = protocol.RPCTimeoutTaskWait
	startupBootstrapWait    = waitForStartupBootstrap
	nodeShutdownTimeout     = 3 * time.Second
)

type bootstrapWaitFunc func(context.Context) bool

type nodeFinalizer struct {
	done chan struct{}
	err  error
}

func waitForStartupBootstrap(ctx context.Context) bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func getSrv() *server.Server {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return srv
}

func getDispatchSrv() *server.Server {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	if appStopping {
		return nil
	}
	return srv
}

// SetStoragePath configures the active storage path for the out-of-process CLI fallback.
func SetStoragePath(path string) {
	srvMutex.Lock()
	appStorage = canonicalStoragePath(path)
	srvMutex.Unlock()
}

// GetStoragePath returns the currently configured storage path.
func GetStoragePath() string {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return appStorage
}

func canonicalStoragePath(path string) string {
	path = canonicalFilesystemPath(path)
	cfg, err := protocol.LoadConfig(path)
	if err == nil && cfg.StoragePath != "" {
		return canonicalFilesystemPath(cfg.StoragePath)
	}
	return path
}

func canonicalFilesystemPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	current := absolute
	var missing []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
	}
	return filepath.Clean(absolute)
}

// dispatchUnixStreamOrLocal runs a streaming action in-process or over unix NDJSON (L2).
// onChunk receives each successful data payload; onError/onComplete follow stream lifecycle.
func dispatchUnixStreamOrLocal(
	ctx context.Context,
	action string,
	args map[string]string,
	localStream func(context.Context, *server.Server, func(map[string]any) error) error,
	onChunkJSON func(chunkJSON string),
	onError func(errMsg string),
	onComplete func(),
	onDone func(),
) {
	s := getDispatchSrv()
	if s != nil {
		leaseCtx, release, leaseErr := s.AcquireWorkLease(ctx)
		if leaseErr != nil {
			go func() {
				if onDone != nil {
					defer onDone()
				}
				if onError != nil {
					onError(leaseErr.Error())
				}
			}()
			return
		}
		go func() {
			defer release()
			if onDone != nil {
				defer onDone()
			}
			err := localStream(leaseCtx, s, func(chunk map[string]any) error {
				if onChunkJSON != nil {
					b, err := json.Marshal(chunk)
					if err != nil {
						return fmt.Errorf("failed to marshal stream chunk: %w", err)
					}
					onChunkJSON(string(b))
				}
				return nil
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

	storagePath := GetStoragePath()
	go func() {
		if onDone != nil {
			defer onDone()
		}
		if err := ctx.Err(); err != nil {
			if onError != nil {
				onError(err.Error())
			}
			return
		}
		conn, err := DialUnix(storagePath)
		if err != nil {
			if onError != nil {
				onError(err.Error())
			}
			return
		}
		defer func() { _ = conn.Close() }()
		stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopClose()

		if err := WriteUnixStreamRequest(conn, action, args); err != nil {
			if onError != nil {
				onError(err.Error())
			}
			return
		}

		selectedVersion := -1
		completed := false
		protocolErr := ""
		scanErr := ScanUnixNDJSON(conn, func(resp protocol.UnixResponse) bool {
			if resp.StreamVersion != 0 && resp.StreamVersion != protocol.ServiceStreamVersion {
				protocolErr = fmt.Sprintf("unsupported Unix stream version %d", resp.StreamVersion)
				return false
			}
			if selectedVersion == -1 {
				selectedVersion = resp.StreamVersion
			} else if selectedVersion != resp.StreamVersion {
				protocolErr = fmt.Sprintf(
					"Unix stream changed framing version from %d to %d",
					selectedVersion,
					resp.StreamVersion,
				)
				return false
			}
			if !resp.Success {
				protocolErr = resp.Error
				return false
			}
			if resp.Complete {
				completed = true
				return false
			}
			if onChunkJSON != nil && resp.Data != nil {
				onChunkJSON(string(resp.Data))
			}
			return true
		})
		if protocolErr != "" {
			if onError != nil {
				onError(protocolErr)
			}
			return
		}
		if scanErr != nil {
			if onError != nil {
				if ctx.Err() != nil {
					onError(ctx.Err().Error())
				} else {
					onError(scanErr.Error())
				}
			}
			return
		}
		if selectedVersion == protocol.ServiceStreamVersion && !completed {
			if onError != nil {
				onError("negotiated v1 Unix stream ended without a terminal event")
			}
			return
		}
		if onComplete != nil {
			onComplete()
		}
	}()
}

// BindErrorJSON formats an error for bind/CLI consumers (SSOT).
func BindErrorJSON(err error) string {
	message := ""
	if err != nil {
		message = err.Error()
	}
	encoded, marshalErr := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	if marshalErr != nil {
		return `{"error":"failed to marshal bind error"}`
	}
	return string(encoded)
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
	b, _ := json.Marshal(protocol.APIMessage{Message: msg})
	return string(b)
}

// dispatchUnixLocalOrOffline is L3: in-process localCall, else unix, else optional offline fallback.
func dispatchUnixLocalOrOffline(
	action string,
	args map[string]string,
	localCall func(s *server.Server) (any, error),
	offline func() (any, error),
) string {
	return dispatchUnixLocalOrOfflineAt(GetStoragePath(), action, args, localCall, offline)
}

func dispatchUnixLocalOrOfflineAt(
	storagePath string,
	action string,
	args map[string]string,
	localCall func(s *server.Server) (any, error),
	offline func() (any, error),
) string {
	s := getDispatchSrv()
	if s != nil {
		_, release, leaseErr := s.AcquireWorkLease(context.Background())
		if leaseErr != nil {
			return bindErrorJSON(leaseErr)
		}
		defer release()
		res, err := localCall(s)
		if err != nil {
			return bindErrorJSON(err)
		}
		return bindOKJSON(res)
	}
	data, err := sendUnixSocketCommand(storagePath, action, args)
	if err == nil {
		return string(data)
	}
	if offline != nil && isDaemonUnavailable(err) {
		res, offErr := offline()
		if offErr != nil {
			return bindErrorJSON(offErr)
		}
		return bindOKJSON(res)
	}
	return bindErrorJSON(err)
}

type daemonUnavailableError struct {
	err error
}

func (e *daemonUnavailableError) Error() string { return e.err.Error() }
func (e *daemonUnavailableError) Unwrap() error { return e.err }

func isDaemonUnavailable(err error) bool {
	var unavailable *daemonUnavailableError
	return errors.As(err, &unavailable)
}

func isUnavailableDialError(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// DialUnix opens a connection to the daemon unix socket (L1).
func DialUnix(storagePath string) (net.Conn, error) {
	socketStorage := storagePath
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("couldn't load config: %w", err)
		}
	} else if cfg.StoragePath != "" {
		socketStorage = canonicalFilesystemPath(cfg.StoragePath)
	}
	sockPath := protocol.UnixSockPath(socketStorage)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		dialErr := fmt.Errorf("daemon is unreachable. Is 'proxyma run' active? Error: %w", err)
		if isUnavailableDialError(err) {
			return nil, &daemonUnavailableError{err: dialErr}
		}
		return nil, dialErr
	}
	return conn, nil
}

// WriteUnixRequest marshals and writes a UnixRequest (L1).
func WriteUnixRequest(conn net.Conn, action string, args map[string]string) error {
	return writeUnixRequest(conn, protocol.UnixRequest{Action: action, Args: args})
}

// WriteUnixStreamRequest advertises supported stream framing versions.
func WriteUnixStreamRequest(conn net.Conn, action string, args map[string]string) error {
	return writeUnixRequest(conn, protocol.UnixRequest{
		Action:         action,
		Args:           args,
		StreamVersions: []int{protocol.ServiceStreamVersion},
	})
}

func writeUnixRequest(conn net.Conn, req protocol.UnixRequest) error {
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
// Each Read gets a fresh idle deadline so large/slow payloads are not cut off by
// a single absolute timeout measured from the start of the call.
func ReadUnixResponse(conn net.Conn) (protocol.UnixResponse, error) {
	var resp protocol.UnixResponse
	var respBytes []byte
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(unixResponseIdleTimeout))
		n, err := conn.Read(buf)
		if n > 0 {
			respBytes = append(respBytes, buf[:n]...)
		}
		if err != nil {
			break
		}
		if len(respBytes) > 32<<20 {
			return resp, fmt.Errorf("daemon response exceeds 32MB")
		}
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return resp, fmt.Errorf("failed to parse daemon response: %w", err)
	}
	return resp, nil
}

// ScanUnixNDJSON scans line-delimited UnixResponse messages (L1).
func ScanUnixNDJSON(conn net.Conn, onLine func(protocol.UnixResponse) bool) error {
	var parseErr error
	err := utils.ScanNDJSON(conn, func(line []byte) bool {
		var resp protocol.UnixResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			parseErr = fmt.Errorf("invalid NDJSON stream chunk: %w", err)
			return false
		}
		return onLine(resp)
	})
	if parseErr != nil {
		return parseErr
	}
	return err
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
	storagePath = canonicalStoragePath(storagePath)
	joinMutex.Lock()
	defer joinMutex.Unlock()
	recoveryErr := recoverJoinInstallation(storagePath)
	if recoveryErr != nil {
		return BindErrorJSON(fmt.Errorf("failed to recover interrupted join: %w", recoveryErr))
	}
	return startNode(storagePath, debug)
}

func startNode(storagePath string, debug bool) string {
	srvMutex.Lock()
	defer srvMutex.Unlock()

	if srv != nil {
		if appStopping {
			return BindErrorJSON(fmt.Errorf("node is stopping"))
		}
		return BindErrorJSON(fmt.Errorf("node is already running"))
	}

	configStorage := canonicalStoragePath(storagePath)
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	logger := protocol.NewLogger(writer, debug)

	cfg, err := protocol.LoadConfig(configStorage)
	if err != nil {
		if os.IsNotExist(err) {
			// Auto initialize configuration with default port if not found
			nid := utils.GenerateDefaultNodeID()
			localAddr := protocol.HTTPSAddr("127.0.0.1", protocol.DefaultTCPPort)
			if err := p2p.SetupNewNode(configStorage, nid, localAddr); err != nil {
				return BindErrorJSON(fmt.Errorf("failed to setup initial node: %v", err))
			}
			cfg, err = protocol.LoadConfig(configStorage)
			if err != nil {
				return BindErrorJSON(fmt.Errorf("failed to load initial config: %v", err))
			}
		} else {
			return BindErrorJSON(fmt.Errorf("failed to load config: %v", err))
		}
	}
	cfg.StoragePath = configStorage
	appStorage = configStorage
	cfg.Logger = logger

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile, nodeKeyFile := p2p.NodeCertPaths(certsDir, cfg.ID)

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return BindErrorJSON(fmt.Errorf("failed to load mTLS certs: %v", err))
	}

	baseTransport := &http.Transport{TLSClientConfig: ctls}
	wrappedTransport := &p2p.BandwidthRoundTripper{Base: baseTransport}
	peerClient := p2p.NewHTTPPeerClient(wrappedTransport, cfg.BootstrapNode, logger)

	startedServer, err := server.New(cfg, peerClient)
	if err != nil {
		return BindErrorJSON(fmt.Errorf("failed to start node: %v", err))
	}
	startedServer.SetTLSConfigs(stls, ctls)
	wrappedTransport.Recorder = startedServer
	startedServer.LoadLocalServices()

	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- startedServer.ListenAndServe(stls)
	}()

	select {
	case <-startedServer.Ready():
	case serveErr := <-serveResult:
		cancel()
		shutdownServerAfterFailedStart(startedServer)
		return BindErrorJSON(fmt.Errorf("failed to start node listeners: %w", serveErr))
	}

	srv = startedServer
	appLogger = logger
	appCtx = ctx
	appCancel = cancel
	appStopping = false
	appFinalizer = nil
	backgroundWork := &sync.WaitGroup{}
	bootstrapWait := startupBootstrapWait
	if cfg.BootstrapNode != "" {
		backgroundWork.Add(1)
	}
	appWork = backgroundWork

	go monitorStartedServer(startedServer, serveResult, cancel, logger, backgroundWork)

	if cfg.BootstrapNode != "" {
		go func() {
			defer backgroundWork.Done()
			runDelayedBootstrap(ctx, startedServer, cfg.BootstrapNode, logger, bootstrapWait)
		}()
	}

	return ""
}

func shutdownServerAfterFailedStart(startedServer *server.Server) {
	_ = startedServer.Shutdown(context.Background())
}

func monitorStartedServer(
	startedServer *server.Server,
	serveResult <-chan error,
	cancel context.CancelFunc,
	logger *slog.Logger,
	backgroundWork *sync.WaitGroup,
) {
	serveErr := <-serveResult
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		logger.Error("Server ListenAndServe failed", "error", serveErr)
	}
	finalizer := ensureNodeFinalizer(startedServer, cancel, backgroundWork)
	if finalizer != nil {
		<-finalizer.done
	}
}

func ensureNodeFinalizer(
	startedServer *server.Server,
	cancel context.CancelFunc,
	backgroundWork *sync.WaitGroup,
) *nodeFinalizer {
	srvMutex.Lock()
	if srv != startedServer {
		srvMutex.Unlock()
		return nil
	}
	if appFinalizer != nil {
		finalizer := appFinalizer
		srvMutex.Unlock()
		return finalizer
	}
	finalizer := &nodeFinalizer{done: make(chan struct{})}
	appStopping = true
	appFinalizer = finalizer
	srvMutex.Unlock()

	go func() {
		if cancel != nil {
			cancel()
		}
		if backgroundWork != nil {
			backgroundWork.Wait()
		}
		shutdownErr := startedServer.Shutdown(context.Background())

		srvMutex.Lock()
		finalizer.err = shutdownErr
		if srv == startedServer && appFinalizer == finalizer {
			clearNodeGlobalsLocked()
		}
		close(finalizer.done)
		srvMutex.Unlock()
	}()
	return finalizer
}

func runDelayedBootstrap(
	ctx context.Context,
	startedServer *server.Server,
	bootstrapNode string,
	logger *slog.Logger,
	wait bootstrapWaitFunc,
) {
	if !wait(ctx) {
		return
	}
	leaseCtx, release, err := startedServer.AcquireWorkLease(ctx)
	if err != nil {
		return
	}
	defer release()
	if err := leaseCtx.Err(); err != nil {
		return
	}
	if err := startedServer.AnnouncePresence(bootstrapNode); err != nil {
		if leaseCtx.Err() == nil {
			logger.Error("AnnouncePresence failed", "error", err)
		}
		return
	}
	if leaseCtx.Err() == nil {
		startedServer.StartRelayPolling(leaseCtx, bootstrapNode)
	}
}

func startNodeBackgroundWork(
	startedServer *server.Server,
	work func(context.Context),
) bool {
	srvMutex.Lock()
	if srv != startedServer || appStopping || appWork == nil || appCtx == nil {
		srvMutex.Unlock()
		return false
	}
	backgroundWork := appWork
	ctx := appCtx
	backgroundWork.Add(1)
	srvMutex.Unlock()

	go func() {
		defer backgroundWork.Done()
		leaseCtx, release, err := startedServer.AcquireWorkLease(ctx)
		if err != nil {
			return
		}
		defer release()
		work(leaseCtx)
	}()
	return true
}

func clearNodeGlobalsLocked() {
	srv = nil
	appCtx = nil
	appCancel = nil
	appWork = nil
	appStopping = false
	appFinalizer = nil
}

// StopNode stops the proxyma node if it is running.
func StopNode() {
	_ = StopNodeWithError()
}

// StopNodeWithError stops the node and returns a bind error if the shutdown
// deadline expires. Globals remain in the stopping state until finalization.
func StopNodeWithError() string {
	srvMutex.RLock()
	if srv == nil {
		srvMutex.RUnlock()
		return ""
	}
	startedServer := srv
	cancelApp := appCancel
	backgroundWork := appWork
	timeout := nodeShutdownTimeout
	srvMutex.RUnlock()

	finalizer := ensureNodeFinalizer(startedServer, cancelApp, backgroundWork)
	if finalizer == nil {
		return ""
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-finalizer.done:
		if finalizer.err != nil {
			return BindErrorJSON(fmt.Errorf("failed to stop node: %w", finalizer.err))
		}
		return ""
	case <-timer.C:
		return BindErrorJSON(fmt.Errorf("failed to stop node: %w", context.DeadlineExceeded))
	}
}

// IsNodeRunning returns true only after both listeners are ready.
func IsNodeRunning() bool {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return srv != nil && !appStopping && srv.IsReady()
}

// IsNodeStopping reports that shutdown started but has not finalized.
func IsNodeStopping() bool {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return srv != nil && appStopping
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

// GetUISchemaJSON exports the full single-source-of-truth UI Schema registry as JSON.
func GetUISchemaJSON() string {
	return uischema.GetRegistryJSON()
}

// GetDomainSchemaJson exports metadata for a specific domain as JSON.
func GetDomainSchemaJson(domainName string) string {
	return uischema.GetDomainJSON(domainName)
}
