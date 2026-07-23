package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/utils"
	"strings"
	"sync"
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

	isSponsor       bool
	checkNATOnce    sync.Once
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
	tlsMutex        sync.RWMutex

	udpConn         *net.UDPConn
	publicUDPAddr   string
	quicMgr         *p2p.QUICManager
	natMapper       *p2p.NATMapper
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) *Server {
	if cfg.Logger == nil {
		panic("server.New: cfg.Logger must not be nil — use protocol.NewLogger to create it")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
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

	if updater, ok := s.peerClient.(interface {
		UpdateSponsorAddress(addr string)
	}); ok {
		updater.UpdateSponsorAddress(cfg.BootstrapNode)
	}

	if setter, ok := s.peerClient.(interface {
		SetNodeID(id string)
	}); ok {
		setter.SetNodeID(cfg.ID)
	}

	if setter, ok := s.peerClient.(interface {
		SetOwnAddress(addr string)
	}); ok {
		setter.SetOwnAddress(cfg.Address)
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Compute.SetAddress(cfg.Address)
	s.Storage = storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers, func(file protocol.IndexEntry, rawSource string) error {
		for peerID, peerAddress := range s.GetPeersCopy() {
			if rawSource == peerAddress {
				s.downloadQueue <- DownloadJob{
					File:   file,
					Source: peerID,
				}
				return nil
			}
		}
		return fmt.Errorf("peer of address %s not found", rawSource)
	})

	// Load persisted peers from DB and populate registry
	if peers, err := s.Storage.LoadPeers(); err == nil {
		for peerID, record := range peers {
			s.Peers.AddPeer(peerID, record)
			s.Peers.SetPeerOffline(peerID, fmt.Errorf("not contacted yet"))
			if updater, ok := s.peerClient.(interface {
				UpdatePeerRoute(peerID string, record protocol.AddressRecord)
			}); ok {
				updater.UpdatePeerRoute(peerID, record)
			}
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
				dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				body, err := s.peerClient.DownloadBlob(dlCtx, requesterNodeID, hash)
				if err != nil {
					return "", fmt.Errorf("failed to download VFS blob %s from %s: %w", hash, requesterNodeID, err)
				}
				defer func() { _ = body.Close() }()
				_, _, err = s.Storage.SavePhysicalBlob(body)
				if err != nil {
					return "", fmt.Errorf("failed to store downloaded VFS blob: %w", err)
				}
			}
		}
		return s.Storage.GetBlobPath(hash), nil
	})
	s.Compute.SetVFSBlobStager(func(pathStr string) (string, int64, error) {
		if _, err := os.Stat(pathStr); err != nil {
			return "", 0, err
		}
		f, err := os.Open(pathStr)
		if err != nil {
			return "", 0, err
		}
		defer func() { _ = f.Close() }()
		hash, size, err := s.Storage.SavePhysicalBlob(f)
		if err != nil {
			return "", 0, err
		}
		s.Storage.Upsert(protocol.IndexEntry{
			Name:    filepath.Base(pathStr),
			Hash:    hash,
			Size:    size,
			Version: 1,
		})
		s.Storage.SetSubscription(filepath.Base(pathStr), true)
		return hash, size, nil
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
	return s
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	go s.listenUnixSocket()

	mux := s.wrapWithBandwidthCounting(s.handler)
	rawAddr := s.Config.Address
	rawAddr = strings.TrimPrefix(rawAddr, "https://")
	rawAddr = strings.TrimPrefix(rawAddr, "http://")
	_, port, err := net.SplitHostPort(rawAddr)
	var addr string
	if err == nil {
		addr = "0.0.0.0:" + port
	} else {
		extractedPort := utils.ExtractPort(s.Config.Address)
		if extractedPort == "" {
			extractedPort = "8080"
		}
		addr = "0.0.0.0:" + extractedPort
	}

	hs := &http.Server{
		Addr:      addr,
		Handler:   mux,
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
		if s.natMapper != nil {
			s.natMapper.Stop()
		}
		if s.httpServer != nil {
			if hsErr := s.httpServer.Shutdown(ctx); hsErr != nil {
				s.Config.Logger.Error("HTTP server shutdown failed", "error", hsErr)
				err = hsErr
				return
			}
		}
		s.Config.Logger.Info("HTTP server stopped accepting connections.")

		if s.Compute != nil {
			s.Compute.Close()
			s.Config.Logger.Info("Compute Engine closed.")
		}

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

func (s *Server) listenUnixSocket() {
	sockPath := filepath.Join(s.Config.StoragePath, "proxyma.sock")
	_ = os.Remove(sockPath) // clean up old socket if it exists
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		s.Config.Logger.Error("Failed to listen on unix socket", "error", err)
		return
	}
	s.unixListener = l
	s.Config.Logger.Info("Listening for local commands on unix socket", "path", sockPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handleUnixConnection(conn)
	}
}

func (s *Server) handleUnixConnection(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	if err != nil {
		return
	}

	// Legacy 1-byte command compat
	if buf[0] == 1 {
		s.Config.Logger.Info("Sync triggered via legacy unix socket command")
		if s.Config.BootstrapNode != "" {
			_ = s.AnnouncePresence(s.Config.BootstrapNode)
		}
		err = s.ExecuteSync()
		if err != nil {
			s.Config.Logger.Error("Sync via legacy unix socket failed", "error", err)
			_, _ = c.Write([]byte{0})
		} else {
			_, _ = c.Write([]byte{1})
		}
		return
	}

	// JSON Request
	if buf[0] == '{' {
		reader := io.MultiReader(bytes.NewReader(buf), c)
		var req protocol.UnixRequest
		if err := json.NewDecoder(reader).Decode(&req); err != nil {
			respBytes, _ := json.Marshal(protocol.UnixResponse{Success: false, Error: "invalid JSON request: " + err.Error()})
			_, _ = c.Write(respBytes)
			return
		}

		var respData any
		var actionErr error

		switch req.Action {
		case "sync":
			if s.Config.BootstrapNode != "" {
				_ = s.AnnouncePresence(s.Config.BootstrapNode)
			}
			actionErr = s.ExecuteSync()

		case "vfs_list":
			respData = s.LocalVFSList()

		case "vfs_upload":
			filePath := req.Args["path"]
			fileName := req.Args["name"]
			if filePath == "" || fileName == "" {
				actionErr = fmt.Errorf("missing path or name parameter")
				break
			}
			f, err := os.Open(filePath)
			if err != nil {
				actionErr = fmt.Errorf("failed to open file: %w", err)
				break
			}
			actionErr = s.Storage.SaveLocalFile(fileName, f)
			_ = f.Close()

		case "vfs_subscribe", "vfs_unsubscribe", "vfs_delete", "vfs_purge", "vfs_fetch":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			switch req.Action {
			case "vfs_subscribe":
				s.Storage.SetSubscription(fileName, true)
				go func() {
					if s.Config.BootstrapNode != "" {
						_ = s.AnnouncePresence(s.Config.BootstrapNode)
					}
					_ = s.ExecuteSync()
				}()
			case "vfs_unsubscribe":
				s.Storage.SetSubscription(fileName, false)
			case "vfs_delete":
				actionErr = s.Storage.DeleteLocalFile(fileName)
			case "vfs_purge":
				actionErr = s.Storage.DeleteLocalCache(fileName)
			case "vfs_fetch":
				actionErr = s.FetchFileOnDemand(fileName)
			}

		case "service_discover":
			respData, actionErr = s.LocalServiceDiscover()

		case "service_detail":
			name := req.Args["name"]
			if name == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			schema, exists := s.Compute.GetService(name)
			if exists {
				respData = schema
				break
			}
			_, _, schema2, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
			if err != nil {
				actionErr = err
				break
			}
			respData = schema2

		case "service_add":
			respData, actionErr = s.LocalServiceAdd(
				req.Args["name"],
				req.Args["type"],
				req.Args["exec"],
				req.Args["desc"],
				req.Args["param"],
				req.Args["no-required"],
				req.Args["schema-file"],
			)

		case "service_remove":
			respData, actionErr = s.LocalServiceRemove(req.Args["name"])

		case "service_run":
			respData, actionErr = s.LocalServiceRun(req.Args["service"], req.Args["payload"])

		case "service_run_file":
			respData, actionErr = s.LocalServiceRunFile(req.Args["service"], req.Args["input"], req.Args["output"], req.Args["param"])

		case "service_status":
			taskID := req.Args["task_id"]
			if taskID == "" {
				respData = s.Compute.GetAllTaskStatuses()
			} else {
				r, ok := s.Compute.GetTaskResponse(taskID)
				if !ok {
					actionErr = fmt.Errorf("task not found")
					break
				}
				respData = r
			}

		case "invite_generate":
			respData, actionErr = s.LocalInviteGenerate(15)

		case "logs":
			protocol.LogBufferMu.RLock()
			logsCopy := make([]protocol.LogRecord, len(protocol.LogBuffer))
			copy(logsCopy, protocol.LogBuffer)
			protocol.LogBufferMu.RUnlock()
			respData = logsCopy

		case "bandwidth":
			respData = s.LocalBandwidthStats()

		case "peers":
			respData = s.LocalPeersList()

		case "pipeline_add":
			actionErr = s.LocalPipelineAdd(req.Args["schema"])

		case "pipeline_validate":
			actionErr = s.LocalPipelineValidate(req.Args["schema"])

		case "pipeline_remove":
			actionErr = s.LocalPipelineRemove(req.Args["id"])

		case "pipeline_list":
			respData = s.LocalPipelineList()

		case "pipeline_get":
			respData, actionErr = s.LocalPipelineGet(req.Args["id"])

		case "pipeline_clone":
			respData, actionErr = s.LocalPipelineClone(req.Args["id"], req.Args["new_id"], req.Args["target_node"])

		default:
			actionErr = fmt.Errorf("unknown action: %s", req.Action)
		}

		var unixResp protocol.UnixResponse
		if actionErr != nil {
			unixResp = protocol.UnixResponse{Success: false, Error: actionErr.Error()}
		} else {
			var raw json.RawMessage
			if respData != nil {
				raw, _ = json.Marshal(respData)
			}
			unixResp = protocol.UnixResponse{Success: true, Data: raw}
		}

		respBytes, _ := json.Marshal(unixResp)
		_, _ = c.Write(respBytes)
	}
}

// Delegated methods for backward compatibility & Continuous Granularity.

func (s *Server) SetPeerOnline(peerID string, online bool) {
	s.Peers.SetPeerOnline(peerID, online)
}

func (s *Server) SetPeerOffline(peerID string, err error) {
	s.Peers.SetPeerOffline(peerID, err)
}

func (s *Server) IsPeerOnline(peerID string) bool {
	return s.Peers.IsPeerOnline(peerID)
}

func (s *Server) RemovePeer(peerID string) {
	s.Peers.RemovePeer(peerID)
	if s.Storage != nil {
		_ = s.Storage.DeletePeer(peerID)
	}
	if updater, ok := s.peerClient.(interface {
		RemovePeerRoute(peerID string)
	}); ok {
		updater.RemovePeerRoute(peerID)
	}
}

func (s *Server) announceOffline(ctx context.Context) {
	peers := s.GetPeersCopy()
	payload := map[string]string{"id": s.Config.ID}
	for peerID := range peers {
		_ = s.peerClient.Offline(ctx, peerID, payload)
	}
}

func (s *Server) GetClusterServices(peerID string) map[string]protocol.ServiceSchema {
	return s.Peers.GetClusterServices(peerID)
}

func (s *Server) SetAddress(addr string) {
	s.Config.Address = addr
	s.Compute.SetAddress(addr)
}

func (s *Server) AddPeer(peerID string, addressRecord protocol.AddressRecord) {
	if s.Peers.AddPeer(peerID, addressRecord) {
		if s.Storage != nil {
			if err := s.Storage.SavePeer(peerID, addressRecord); err != nil {
				s.Config.Logger.Error("Failed to save peer to DB", "peerID", peerID, "error", err)
			}
		}
		if updater, ok := s.peerClient.(interface {
			UpdatePeerRoute(peerID string, record protocol.AddressRecord)
		}); ok {
			updater.UpdatePeerRoute(peerID, addressRecord)
		}
		go func(targetPeer string) {
			for _, schema := range s.Compute.ListPipelines() {
				s.NotifySchemaToPeer(targetPeer, schema, "add")
			}
		}(peerID)
	}
}

func (s *Server) GetPeersCopy() map[string]string {
	return s.Peers.GetPeersCopy()
}

func (s *Server) GetSponsorPeers() map[string]string {
	return s.Peers.GetSponsorPeers()
}

func (s *Server) AnnouncePresence(sponsorAddress string) error {
	s.CheckNAT()
	addresses := []string{s.Config.Address}
	if s.isSponsor && s.publicUDPAddr != "" {
		host, _, err := net.SplitHostPort(s.publicUDPAddr)
		if err == nil {
			tcpPortStr := utils.ExtractPort(s.Config.Address)
			if s.natMapper != nil {
				if mappedTCP, _ := s.natMapper.GetMappedPorts(); mappedTCP > 0 {
					tcpPortStr = fmt.Sprintf("%d", mappedTCP)
				}
			}
			publicTCPAddr := fmt.Sprintf("https://%s:%s", host, tcpPortStr)
			addresses = append(addresses, publicTCPAddr)
		}
	}
	if s.publicUDPAddr != "" {
		addresses = append(addresses, "quic://"+s.publicUDPAddr)
	}
	payload := protocol.AddPeerRequest{
		ID: s.Config.ID,
		Address: protocol.AddressRecord{
			Addresses: addresses,
			IsSponsor: s.isSponsor,
		},
	}

	announceResp, err := s.peerClient.Announce(sponsorAddress, payload)
	if err != nil {
		s.Config.Logger.Error("Error while announcing from sponsor", "sponsor", sponsorAddress, "error", err)
		return fmt.Errorf("there was an error trying to connect to the cluster: %v", err)
	}
	s.Config.Logger.Info("AnnounceResp received without errors", "resp", announceResp)
	for id, addrRec := range announceResp {
		if id != s.Config.ID {
			s.AddPeer(id, addrRec)
		}
	}
	s.Config.Logger.Info("Successfully synced topology from sponsor", "peers_count", len(announceResp))
	go func() {
		_ = s.ExecuteSync()
	}()
	return nil
}

func (s *Server) CheckNAT() {
	s.checkNATOnce.Do(func() {
		s.determineSponsorAndNATStatus()
	})
}

func (s *Server) AddPendingInvite(secret string, expiration time.Time) {
	s.Invites.Add(secret, expiration)
}

func (s *Server) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return s.peerClient.DiscoverServices(ctx, peerID)
}

func (s *Server) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	return s.Peers.GetPeerRecord(peerID)
}

func (s *Server) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	return s.Peers.GetPeersRecordCopy()
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

func (s *Server) SetTLSConfigs(serverTLS, clientTLS *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.serverTLSConfig = serverTLS
	s.clientTLSConfig = clientTLS
}

func (s *Server) ReloadTLSConfig(caPath, certPath, keyPath string) error {
	newServerTLS, newClientTLS, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	if err != nil {
		return fmt.Errorf("failed to load rotated TLS certs: %w", err)
	}

	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()

	s.Config.Logger.Info("Reloading dynamic TLS configuration across server and client...")

	if s.serverTLSConfig != nil {
		s.serverTLSConfig.Certificates = newServerTLS.Certificates
		s.serverTLSConfig.ClientCAs = newServerTLS.ClientCAs
	}

	if s.clientTLSConfig != nil {
		s.clientTLSConfig.Certificates = newClientTLS.Certificates
		s.clientTLSConfig.RootCAs = newClientTLS.RootCAs
		s.clientTLSConfig.VerifyPeerCertificate = newClientTLS.VerifyPeerCertificate
	}

	return nil
}

func (s *Server) RotateCAAndResignPeers() {
	caKeyPath := strings.Replace(s.Config.CAPath, ".crt", ".key", 1)
	if _, err := os.Stat(caKeyPath); err != nil {
		s.Config.Logger.Debug("We are not the CA authority node, skipping CA rotation")
		return
	}

	s.Config.Logger.Info("Triggering CA Rotation & Peer Re-signing...")

	// 1. Rotate the CA files
	certsDir := filepath.Dir(s.Config.CAPath)
	err := p2p.RotateCA(certsDir)
	if err != nil {
		s.Config.Logger.Error("Failed to rotate CA", "error", err)
		return
	}

	// 2. Re-sign own certificate with the new CA
	ownCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", s.Config.ID))
	ownKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", s.Config.ID))
	err = p2p.IssueNodeCertificate(certsDir, certsDir, s.Config.ID)
	if err != nil {
		s.Config.Logger.Error("Failed to re-sign own certificate", "error", err)
		return
	}

	// 3. Loop over all other registered peers and re-sign their certificates
	peers := s.GetPeersCopy()
	var wg sync.WaitGroup

	for peerID, addr := range peers {
		if peerID == s.Config.ID {
			continue
		}

		cert, hasCert := s.Peers.GetPeerCertificate(peerID)
		if !hasCert {
			s.Config.Logger.Warn("No client certificate cached for peer, cannot re-sign. They must re-join.", "peerID", peerID)
			continue
		}

		wg.Add(1)
		go func(pid, paddr string, pcert *x509.Certificate) {
			defer wg.Done()

			newCertPEM, err := p2p.ReSignPeerCertificate(pcert.PublicKey, pid, s.Config.CAPath, caKeyPath)
			if err != nil {
				s.Config.Logger.Error("Failed to re-sign peer certificate", "peerID", pid, "error", err)
				return
			}

			caCertPEM, err := os.ReadFile(s.Config.CAPath)
			if err != nil {
				return
			}

			rotationPayload := map[string]string{
				"ca_cert":   string(caCertPEM),
				"node_cert": string(newCertPEM),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err = s.peerClient.RotateTLS(ctx, pid, rotationPayload)
			if err != nil {
				s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", pid, "error", err)
			} else {
				s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", pid)
			}
		}(peerID, addr, cert)
	}

	wg.Wait()

	// 4. Finally, reload our own TLS config in place
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}


