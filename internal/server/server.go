package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/telemetry"
	"proxyma/internal/utils"
	"slices"
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

	handler       http.Handler
	httpServer    *http.Server
	downloadQueue chan DownloadJob
	unixListener  net.Listener
	done          chan struct{}
	shutdownOnce  sync.Once

	routeIndexOnce sync.Once
	routePolicies  map[string]routePolicy

	isSponsor       bool
	checkNATOnce    sync.Once
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
	tlsMutex        sync.RWMutex
	clientMaterial  atomic.Pointer[tlsClientMaterial]
	serverMaterial  atomic.Pointer[tlsServerMaterial]

	udpConn       *net.UDPConn
	publicUDPAddr string
	quicMgr       *p2p.QUICManager
	natMapper     *p2p.NATMapper
	natMu         sync.Mutex

	webrtcPCs   sync.Map
	webrtcPCSeq uint64

	outboxFlushMu sync.Mutex
}

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
	s := &Server{
		Config:        cfg,
		peerClient:    peerClient,
		downloadQueue: make(chan DownloadJob, 100),
		done:          make(chan struct{}),
	}

	s.Peers = NewPeerRegistry(cfg.Logger, cfg.ID)
	s.Invites = NewInviteManager(cfg.Logger)
	s.Bandwidth = NewBandwidthTracker()
	s.Relays = NewRelayManager(s)

	if s.peerClient != nil {
		s.peerClient.UpdateSponsorAddress(cfg.BootstrapNode)
		s.peerClient.SetNodeID(cfg.ID)
		s.peerClient.SetOwnAddress(cfg.Address)
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Compute.SetAddress(cfg.Address)
	engine, err := storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.notifyPeers, func(file protocol.IndexEntry, rawSource string) error {
		for peerID, record := range s.GetPeersRecordCopy() {
			if slices.Contains(record.Addresses, rawSource) {
				return s.enqueueDownload(DownloadJob{File: file, Source: peerID})
			}
		}
		return fmt.Errorf("peer of address %s not found", rawSource)
	})
	if err != nil {
		s.Compute.Close()
		return nil, err
	}
	s.Storage = engine

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
			s.Compute.RegisterPipeline(schema)
		}
	} else {
		cfg.Logger.Error("Failed to load persisted pipeline schemas", "error", err)
	}

	s.handler = s.MountHandlers()

	for range cfg.Workers {
		go s.downloadWorker()
	}
	s.startOutboxWorker()

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.Invites.Sweep()
				s.Storage.CleanupTempFiles()
			}
		}
	}()
	s.Storage.CleanupTempFiles()
	return s, nil
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	go s.listenUnixSocket()

	portStr, _ := s.configTCPPort()
	addr := "0.0.0.0:" + portStr

	// http.Server.ServeTLS clones TLSConfig. Static Certificates/ClientCAs on the
	// original pointer are therefore invisible to the listener after start.
	// GetConfigForClient is copied by Clone and reloads leaf+CA from disk per handshake,
	// so RotateCAAndResignPeers / ReloadTLSConfig stay effective without restart.
	s.armHotReloadServerTLS(serverTLS)

	hs := &http.Server{
		Addr:      addr,
		Handler:   s.handler, // MountHandlers already wraps bandwidth + mTLS
		TLSConfig: serverTLS,
		ErrorLog:  log.New(&tlsErrorWriter{server: s}, "", 0),
	}

	s.httpServer = hs
	s.Config.Logger.Info("Starting secure P2P node", "address", addr)

	// Run NAT auto-detection asynchronously after a brief delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.CheckNAT()
	}()

	return hs.ListenAndServeTLS("", "")
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
	var err error
	s.shutdownOnce.Do(func() {
		s.Config.Logger.Info("Initiating shutdown...")
		close(s.done)
		s.announceOffline(ctx)
		s.natMu.Lock()
		nm := s.natMapper
		s.natMu.Unlock()
		if nm != nil {
			nm.Stop()
		}
		if s.httpServer != nil {
			if hsErr := s.httpServer.Shutdown(ctx); hsErr != nil {
				s.Config.Logger.Error("HTTP server shutdown failed", "error", hsErr)
				err = hsErr
			} else {
				s.Config.Logger.Info("HTTP server stopped accepting connections.")
			}
		}

		if s.Compute != nil {
			s.Compute.Close()
			s.Config.Logger.Info("Compute Engine closed.")
		}

		s.closeWebRTCPeers()

		if s.quicMgr != nil {
			s.quicMgr.Close()
			s.Config.Logger.Info("QUIC Manager closed.")
		}

		if s.unixListener != nil {
			_ = s.unixListener.Close()
		}

		if s.Storage != nil {
			_ = s.Storage.Close()
			s.Config.Logger.Info("Storage Engine closed.")
		}

		s.Config.Logger.Info("Node shutdown complete.")
	})
	return err
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
	select {
	case <-s.done:
		return fmt.Errorf("download queue closed: shutting down")
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
