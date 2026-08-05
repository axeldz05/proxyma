package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
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

	if s.peerClient != nil {
		s.peerClient.UpdateSponsorAddress(cfg.BootstrapNode)
		s.peerClient.SetNodeID(cfg.ID)
		s.peerClient.SetOwnAddress(cfg.Address)
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
	s.peerClient.RemovePeerRoute(peerID)
}

func (s *Server) announceOffline(ctx context.Context) {
	payload := map[string]string{"id": s.Config.ID}
	for peerID := range s.GetPeersCopy() {
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
		s.peerClient.UpdatePeerRoute(peerID, addressRecord)
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




