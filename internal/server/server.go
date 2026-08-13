package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/telemetry"
	"proxyma/internal/utils"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	Config     protocol.NodeConfig
	Compute    *compute.ComputeEngine
	Storage    *storage.StorageEngine
	peerClient p2p.PeerClient

	Peers     *PeerRegistry
	Invites   *InviteManager
	Bandwidth *BandwidthTracker
	Relays    *RelayManager

	handler             http.Handler
	httpServer          *http.Server
	httpListener        net.Listener
	downloadQueue       chan DownloadJob
	unixListener        net.Listener
	ready               chan struct{}
	readyOnce           sync.Once
	done                chan struct{}
	shutdownOnce        sync.Once
	shutdownDone        chan struct{}
	shutdownErr         error
	shutdownRequested   atomic.Bool
	lifetimeCtx         context.Context
	cancelLife          context.CancelFunc
	downloadWG          sync.WaitGroup
	lifecycleMu         sync.Mutex
	shutdownStarted     bool
	listenFunc          func(network, address string) (net.Listener, error)
	listenerWG          sync.WaitGroup
	httpShutdownStarted chan struct{}
	tcpFamilies         tcpFamily
	workMu              sync.Mutex
	acceptingWork       bool
	workWG              sync.WaitGroup
	unixConnMu          sync.Mutex
	unixConns           map[net.Conn]struct{}

	routeIndexOnce sync.Once
	routePolicies  map[string]routePolicy

	isSponsor       bool
	checkNATOnce    sync.Once
	natCheck        func(context.Context)
	natWorkMu       sync.Mutex
	natWG           sync.WaitGroup
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
	tlsMutex        sync.RWMutex
	clientMaterial  atomic.Pointer[tlsClientMaterial]
	serverMaterial  atomic.Pointer[tlsServerMaterial]

	udpConn          *net.UDPConn
	publicUDPAddr    string
	quicMgr          *p2p.QUICManager
	natMapper        *p2p.NATMapper
	natMapperFactory func(*slog.Logger, int, int) *p2p.NATMapper
	natMu            sync.RWMutex

	webrtcPCs   sync.Map
	webrtcPCSeq uint64

	outboxFlushMu sync.Mutex
}

type tcpFamily uint8

const (
	tcpFamilyIPv4 tcpFamily = 1 << iota
	tcpFamilyIPv6
)

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) (*Server, error) {
	if cfg.Logger == nil {
		return nil, errors.New("server.New: cfg.Logger must not be nil — use protocol.NewLogger to create it")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	telemetry.InitFromEnv()
	lifetimeCtx, cancelLife := context.WithCancel(context.Background())
	s := &Server{
		Config:        cfg,
		peerClient:    peerClient,
		downloadQueue: make(chan DownloadJob, 100),
		ready:         make(chan struct{}),
		done:          make(chan struct{}),
		shutdownDone:  make(chan struct{}),
		lifetimeCtx:   lifetimeCtx,
		cancelLife:    cancelLife,
		listenFunc:    net.Listen,
		acceptingWork: true,
		unixConns:     make(map[net.Conn]struct{}),
		tcpFamilies:   tcpFamilyIPv4,
	}
	s.natCheck = s.determineSponsorAndNATStatus

	s.Peers = NewPeerRegistry(cfg.Logger, cfg.ID)
	s.Bandwidth = NewBandwidthTracker()
	s.Relays = NewRelayManager(s)

	if s.peerClient != nil {
		s.peerClient.SetLifetimeContext(lifetimeCtx)
		s.peerClient.UpdateSponsorAddress(cfg.BootstrapNode)
		s.peerClient.SetNodeID(cfg.ID)
		s.peerClient.SetOwnAddress(cfg.Address)
	}

	var err error
	s.Compute, err = compute.NewComputeEngine(lifetimeCtx, cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	if err != nil {
		s.cancelLife()
		return nil, err
	}
	s.Compute.SetAddress(cfg.Address)
	engine, err := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, nil, func(file protocol.IndexEntry, rawSource string) error {
		if _, ok := s.GetPeersRecordCopy()[rawSource]; ok {
			return s.enqueueDownload(DownloadJob{File: file, Source: rawSource})
		}
		for peerID, record := range s.GetPeersRecordCopy() {
			if slices.Contains(record.Addresses, rawSource) {
				return s.enqueueDownload(DownloadJob{File: file, Source: peerID})
			}
		}
		return fmt.Errorf("peer of address %s not found", rawSource)
	})
	if err != nil {
		s.Compute.Close()
		s.cancelLife()
		return nil, err
	}
	s.Storage = engine
	s.Storage.SetMutationNotificationHook(s.prepareVFSNotification)
	s.Invites, err = NewInviteManager(cfg.Logger, s.Storage)
	if err != nil {
		_ = s.Storage.Close()
		s.Compute.Close()
		s.cancelLife()
		return nil, fmt.Errorf("initialize invite manager: %w", err)
	}

	// Load persisted peers from DB and populate registry
	if peers, err := s.Storage.LoadPeers(); err == nil {
		for peerID, record := range peers {
			_, _ = s.Peers.AddPeer(peerID, record)
			s.Peers.SetPeerOffline(peerID, fmt.Errorf("not contacted yet"))
			s.peerClient.UpdatePeerRoute(peerID, record)
		}
	} else {
		cfg.Logger.Error("Failed to load persisted peers", "error", err)
	}

	// Configure compute callbacks
	s.Compute.SetServiceFinder(s.RequestServiceToCluster)
	s.Compute.SetTaskDispatcher(s.DispatchTask)
	s.Compute.SetVFSBlobResolver(func(ctx context.Context, requesterNodeID, hash string) (string, error) {
		hasLocal, _ := s.Storage.HasPhysicalBlob(hash)
		if !hasLocal {
			if requesterNodeID != "" && requesterNodeID != s.Config.ID {
				entry := protocol.IndexEntry{Hash: hash}
				if err := s.fetchBlobFromPeer(ctx, requesterNodeID, entry); err != nil {
					return "", fmt.Errorf("failed to download VFS blob %s from %s: %w", hash, requesterNodeID, err)
				}
			}
		}
		hasLocal, _ = s.Storage.HasPhysicalBlob(hash)
		if !hasLocal {
			return "", fmt.Errorf("VFS blob %s not available locally", hash)
		}
		return s.Storage.GetBlobPath(hash), nil
	})
	s.Compute.SetVFSBlobStager(func(pathStr string) (string, int64, error) {
		return s.Storage.StageLocalFile(pathStr)
	})

	// Load persisted pipeline schemas
	if schemas, err := s.Storage.LoadPipelineSchemas(); err == nil {
		for _, schema := range schemas {
			if err := s.Compute.RegisterPipeline(schema); err != nil {
				cfg.Logger.Error(
					"Failed to register persisted pipeline schema",
					"pipelineID", schema.ID,
					"version", schema.Version,
					"error", err,
				)
			}
		}
	} else {
		cfg.Logger.Error("Failed to load persisted pipeline schemas", "error", err)
	}

	s.handler = s.trackHTTPHandler(s.MountHandlers())

	s.downloadWG.Add(cfg.Workers)
	for range cfg.Workers {
		go s.downloadWorker()
	}
	s.startOutboxWorker()

	s.goOwned(func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				if err := s.Invites.Sweep(); err != nil {
					s.Config.Logger.Error("Failed to sweep expired invites", "error", err)
				}
				s.Storage.CleanupTempFiles()
			}
		}
	})
	s.Storage.CleanupTempFiles()
	return s, nil
}

func (s *Server) beginOwnedWork() bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if !s.acceptingWork {
		return false
	}
	s.workWG.Add(1)
	return true
}

func (s *Server) finishOwnedWork() {
	s.workWG.Done()
}

// AcquireWorkLease joins direct in-process work to the server lifetime and shutdown barrier.
func (s *Server) AcquireWorkLease(ctx context.Context) (context.Context, func(), error) {
	if !s.beginOwnedWork() {
		return nil, nil, http.ErrServerClosed
	}
	leaseCtx, cancel := s.contextWithServerLifetime(ctx)
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			s.finishOwnedWork()
		})
	}
	return leaseCtx, release, nil
}

func (s *Server) beginUnixWork(conn net.Conn) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if !s.acceptingWork {
		return false
	}
	s.workWG.Add(1)
	s.unixConnMu.Lock()
	s.unixConns[conn] = struct{}{}
	s.unixConnMu.Unlock()
	return true
}

func (s *Server) goOwned(fn func()) bool {
	if !s.beginOwnedWork() {
		return false
	}
	go func() {
		defer s.finishOwnedWork()
		fn()
	}()
	return true
}

func (s *Server) stopAcceptingOwnedWork() {
	s.workMu.Lock()
	s.acceptingWork = false
	s.workMu.Unlock()
}

func (s *Server) trackHTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.beginOwnedWork() {
			http.Error(w, http.ErrServerClosed.Error(), http.StatusServiceUnavailable)
			return
		}
		defer s.finishOwnedWork()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	if s.shutdownRequested.Load() {
		return http.ErrServerClosed
	}
	s.lifecycleMu.Lock()
	if s.shutdownRequested.Load() || s.shutdownStarted {
		s.lifecycleMu.Unlock()
		return http.ErrServerClosed
	}
	portStr, _ := s.configTCPPort()
	addr := ":" + portStr

	// http.Server.ServeTLS clones TLSConfig. Static Certificates/ClientCAs on the
	// original pointer are therefore invisible to the listener after start.
	// GetConfigForClient is copied by Clone and reloads leaf+CA from disk per handshake,
	// so RotateCAAndResignPeers / ReloadTLSConfig stay effective without restart.
	s.armHotReloadServerTLS(serverTLS)

	sockPath := protocol.UnixSockPath(s.Config.StoragePath)
	_ = os.Remove(sockPath)
	unixListener, err := s.listenFunc("unix", sockPath)
	if err != nil {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("listen unix socket: %w", err)
	}
	if unixListener.Addr() != nil && unixListener.Addr().Network() == "unix" {
		if err := os.Chmod(sockPath, 0o600); err != nil {
			_ = unixListener.Close()
			_ = os.Remove(sockPath)
			s.lifecycleMu.Unlock()
			return fmt.Errorf("secure unix socket: %w", err)
		}
	}
	httpListener, err := s.listenFunc("tcp", addr)
	if err != nil {
		_ = unixListener.Close()
		_ = os.Remove(sockPath)
		s.lifecycleMu.Unlock()
		return fmt.Errorf("listen TCP: %w", err)
	}
	if portStr == "0" {
		boundAddress, boundErr := addressWithListenerPort(s.Config.Address, httpListener)
		if boundErr != nil {
			_ = httpListener.Close()
			_ = unixListener.Close()
			_ = os.Remove(sockPath)
			s.lifecycleMu.Unlock()
			return fmt.Errorf("resolve bound TCP address: %w", boundErr)
		}
		s.SetAddress(boundAddress)
		if err := protocol.SaveConfig(s.Config); err != nil {
			_ = httpListener.Close()
			_ = unixListener.Close()
			_ = os.Remove(sockPath)
			s.lifecycleMu.Unlock()
			return fmt.Errorf("persist bound TCP address: %w", err)
		}
		addr = ":" + strconv.Itoa(httpListener.Addr().(*net.TCPAddr).Port)
	}

	hs := &http.Server{
		Addr:      addr,
		Handler:   s.handler, // MountHandlers already wraps bandwidth + mTLS
		TLSConfig: serverTLS,
		ErrorLog:  log.New(&tlsErrorWriter{server: s}, "", 0),
	}
	httpShutdownStarted := make(chan struct{})
	hs.RegisterOnShutdown(func() { close(httpShutdownStarted) })

	s.httpServer = hs
	s.httpListener = httpListener
	s.tcpFamilies = listenerTCPFamilies(httpListener)
	s.httpShutdownStarted = httpShutdownStarted
	s.unixListener = unixListener
	s.listenerWG.Add(1)
	go s.serveUnixListener(unixListener, sockPath)
	s.readyOnce.Do(func() { close(s.ready) })
	s.Config.Logger.Info("Starting secure P2P node", "address", addr)

	// Listeners are ready; NAT auto-detection can start immediately in background.
	s.scheduleNATCheck(0)
	s.lifecycleMu.Unlock()

	return hs.ServeTLS(httpListener, "", "")
}

func addressWithListenerPort(configured string, listener net.Listener) (string, error) {
	parsed, err := url.Parse(configured)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid configured address %q", configured)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.Port <= 0 {
		return "", fmt.Errorf("listener has no bound TCP port")
	}
	return protocol.HTTPSAddr(parsed.Hostname(), strconv.Itoa(tcpAddress.Port)), nil
}

// Ready closes after both Unix and TCP listeners have bound successfully.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// IsReady reports listener readiness while the server is still active.
func (s *Server) IsReady() bool {
	if s.shutdownRequested.Load() {
		return false
	}
	select {
	case <-s.ready:
		return true
	default:
		return false
	}
}

func listenerTCPFamilies(listener net.Listener) tcpFamily {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.IP == nil {
		return tcpFamilyIPv4
	}
	if addr.IP.To4() != nil {
		return tcpFamilyIPv4
	}
	if addr.IP.IsUnspecified() {
		// Go requests an IPv4-mapped IPv6 wildcard on platforms that support
		// dual-stack sockets, and falls back before returning the listener.
		return tcpFamilyIPv4 | tcpFamilyIPv6
	}
	return tcpFamilyIPv6
}

func (s *Server) currentTCPFamilies() tcpFamily {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.tcpFamilies
}

func (s *Server) serveUnixListener(listener net.Listener, sockPath string) {
	defer s.listenerWG.Done()
	defer func() { _ = os.Remove(sockPath) }()
	s.Config.Logger.Info("Listening for local commands on unix socket", "path", sockPath)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if !s.beginUnixWork(conn) {
			_ = conn.Close()
			continue
		}
		go func() {
			defer s.finishOwnedWork()
			defer func() {
				s.unixConnMu.Lock()
				delete(s.unixConns, conn)
				s.unixConnMu.Unlock()
			}()
			s.handleUnixConnection(conn)
		}()
	}
}

func (s *Server) closeUnixConnections() {
	s.unixConnMu.Lock()
	defer s.unixConnMu.Unlock()
	for conn := range s.unixConns {
		_ = conn.Close()
	}
}

type tlsErrorWriter struct {
	server *Server
}

func (w *tlsErrorWriter) Write(p []byte) (n int, err error) {
	line := string(p)
	if strings.Contains(line, "TLS handshake error") {
		parts := strings.SplitN(line, "TLS handshake error from ", 2)
		if len(parts) == 2 {
			addrPart := parts[1]
			addrErrParts := strings.SplitN(addrPart, ": ", 2)
			if len(addrErrParts) == 2 {
				hostPort := addrErrParts[0]
				errMsg := addrErrParts[1]

				host, _, err := net.SplitHostPort(hostPort)
				if err != nil {
					host = hostPort
				}

				peerID := ""
				peerAddr := ""
				for id, pRecord := range w.server.Peers.GetPeersRecordCopy() {
					for _, a := range pRecord.Addresses {
						if strings.Contains(a, host) {
							peerID = id
							peerAddr = a
							break
						}
					}
					if peerID != "" {
						break
					}
				}

				if peerID != "" {
					w.server.Config.Logger.Error("TLS Handshake error from registered peer",
						"peerID", peerID,
						"peerAddress", peerAddr,
						"remoteAddr", hostPort,
						"error", strings.TrimSpace(errMsg),
					)
					w.server.SetPeerOffline(peerID, fmt.Errorf("TLS handshake failed: %s", strings.TrimSpace(errMsg)))
					return len(p), nil
				} else {
					w.server.Config.Logger.Error("TLS Handshake error from unknown source",
						"remoteAddr", hostPort,
						"error", strings.TrimSpace(errMsg),
					)
					return len(p), nil
				}
			}
		}
	}
	w.server.Config.Logger.Error("HTTP server error", "message", strings.TrimSpace(line))
	return len(p), nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.shutdownRequested.Store(true)
		go s.finishShutdown(ctx)
	})

	select {
	case <-s.shutdownDone:
		s.lifecycleMu.Lock()
		err := s.shutdownErr
		s.lifecycleMu.Unlock()
		return err
	default:
	}
	select {
	case <-s.shutdownDone:
		s.lifecycleMu.Lock()
		err := s.shutdownErr
		s.lifecycleMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ShutdownDone closes only after all server-owned resources have finalized.
func (s *Server) ShutdownDone() <-chan struct{} {
	return s.shutdownDone
}

func (s *Server) finishShutdown(ctx context.Context) {
	s.Config.Logger.Info("Initiating shutdown...")
	s.lifecycleMu.Lock()
	s.shutdownStarted = true
	close(s.done)
	httpServer := s.httpServer
	httpListener := s.httpListener
	httpShutdownStarted := s.httpShutdownStarted
	unixListener := s.unixListener
	s.stopAcceptingOwnedWork()
	s.lifecycleMu.Unlock()

	s.cancelServerLifetime()
	if unixListener != nil {
		_ = unixListener.Close()
	}
	s.closeUnixConnections()

	var httpShutdownDone chan error
	if httpServer != nil {
		httpShutdownDone = make(chan error, 1)
		go func() {
			httpShutdownDone <- httpServer.Shutdown(ctx)
		}()
		if httpShutdownStarted != nil {
			select {
			case <-httpShutdownStarted:
			case <-ctx.Done():
			}
		}
	}
	if httpListener != nil {
		_ = httpListener.Close()
	}

	offlineDone := make(chan struct{})
	go func() {
		defer close(offlineDone)
		s.announceOffline(ctx)
	}()
	s.stopNATWork()

	if s.Compute != nil {
		s.Compute.Close()
		s.Config.Logger.Info("Compute Engine closed.")
	}
	s.downloadWG.Wait()

	var shutdownErr error
	if httpShutdownDone != nil {
		select {
		case shutdownErr = <-httpShutdownDone:
		case <-ctx.Done():
			shutdownErr = ctx.Err()
		}
		if shutdownErr != nil {
			_ = httpServer.Close()
			s.Config.Logger.Error("HTTP server shutdown failed", "error", shutdownErr)
		} else {
			s.Config.Logger.Info("HTTP server stopped accepting connections.")
		}
	}

	s.listenerWG.Wait()
	s.workWG.Wait()
	<-offlineDone
	if s.peerClient != nil {
		s.peerClient.Close()
	}
	s.closeWebRTCPeers()

	if qm := s.detachQUICManager(); qm != nil {
		qm.Close()
		s.Config.Logger.Info("QUIC Manager closed.")
	}

	if s.Storage != nil {
		_ = s.Storage.Close()
		s.Config.Logger.Info("Storage Engine closed.")
	}

	s.Config.Logger.Info("Node shutdown complete.")
	s.lifecycleMu.Lock()
	s.shutdownErr = shutdownErr
	close(s.shutdownDone)
	s.lifecycleMu.Unlock()
}

// shuttingDown reports whether Shutdown has closed the done channel.
func (s *Server) shuttingDown() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// enqueueDownload non-blockingly queues a blob download; drops when full or shutting down.
func (s *Server) enqueueDownload(job DownloadJob) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shutdownStarted {
		return fmt.Errorf("download queue closed: shutting down")
	}
	select {
	case s.downloadQueue <- job:
		return nil
	default:
		return fmt.Errorf("download queue full (hash=%s source=%s)", job.File.Hash, job.Source)
	}
}

func (s *Server) RecordBytesSent(n int64, path string) {
	s.Bandwidth.RecordBytesSent(n, path)
}

func (s *Server) RecordBytesReceived(n int64, path string) {
	s.Bandwidth.RecordBytesReceived(n, path)
}

func (s *Server) GetCurrentBandwidth() (float64, float64) {
	return s.Bandwidth.GetCurrentBandwidth()
}

func (s *Server) GetCategoryBandwidth(category string) (float64, float64) {
	return s.Bandwidth.GetCategoryBandwidth(category)
}

func (s *Server) GetTotalBandwidth() (int64, int64) {
	return s.Bandwidth.GetTotalBandwidth()
}

type countingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int64
	onWrite      func(int)
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	if w.onWrite != nil && n > 0 {
		w.onWrite(n)
	}
	return n, err
}

// Flush preserves http.Flusher from the underlying writer (NDJSON /services/stream).
func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) wrapWithBandwidthCounting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = &utils.CountingReadCloser{
				ReadCloser: r.Body,
				OnRead: func(n int) {
					s.RecordBytesReceived(int64(n), r.URL.RequestURI())
				},
			}
		}

		cw := &countingResponseWriter{
			ResponseWriter: w,
			onWrite: func(n int) {
				s.RecordBytesSent(int64(n), r.URL.RequestURI())
			},
		}
		next.ServeHTTP(cw, r)
	})
}
