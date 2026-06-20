package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
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
	Config        protocol.NodeConfig
	Compute       *compute.ComputeEngine
	Storage       *storage.StorageEngine
	peerClient    p2p.PeerClient
	
	Peers         *PeerRegistry
	Invites       *InviteManager
	Bandwidth     *BandwidthTracker
	Relays        *RelayManager

	handler       http.Handler
	httpServer    *http.Server
	downloadQueue chan DownloadJob
	unixListener  net.Listener
	done          chan struct{}
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) *Server {
	if cfg.Logger == nil {
		panic("server.New: cfg.Logger must not be nil — use protocol.NewLogger to create it")
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

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
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
			}
		}
	}()
	
	return s
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	go s.listenUnixSocket()

	mux := s.wrapWithBandwidthCounting(s.handler)
	addr := fmt.Sprintf(":%s", strings.Split(s.Config.Address, ":")[2])

	hs := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: serverTLS,
		ErrorLog:  slog.NewLogLogger(s.Config.Logger.Handler(), slog.LevelError),
	}

	s.httpServer = hs
	s.Config.Logger.Info("Starting secure P2P node", "address", addr)

	return hs.ListenAndServeTLS("", "")
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.Config.Logger.Info("Initiating shutdown...")
	close(s.done)
	s.announceOffline(ctx)
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.Config.Logger.Error("HTTP server shutdown failed", "error", err)
			return err
		}
	}
	s.Config.Logger.Info("HTTP server stopped accepting connections.")

	if s.Compute != nil {
		s.Compute.Close()
		s.Config.Logger.Info("Compute Engine closed.")
	}

	if s.unixListener != nil {
		_ = s.unixListener.Close()
	}

	s.Config.Logger.Info("Node shutdown complete.")
	return nil
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
	_, err := c.Read(buf) // wait for any byte
	if err != nil {
		return
	}
	s.Config.Logger.Info("Sync triggered via unix socket")
	err = s.ExecuteSync()
	if err != nil {
		s.Config.Logger.Error("Sync via unix socket failed", "error", err)
		_, _ = c.Write([]byte{0})
	} else {
		_, _ = c.Write([]byte{1})
	}
}

func (s *Server) LoadLocalServices() {
	servicesFile := filepath.Join(s.Config.StoragePath, "services.json")
	data, err := os.ReadFile(servicesFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.Config.Logger.Info("No services.json found, skipping local service registration")
			return
		}
		s.Config.Logger.Error("Failed to read services.json", "error", err)
		return
	}

	type LocalService struct {
		Type   string                 `json:"type"`
		Exec   string                 `json:"exec,omitempty"`
		Schema protocol.ServiceSchema `json:"schema"`
	}

	var services map[string]LocalService
	if err := json.Unmarshal(data, &services); err != nil {
		s.Config.Logger.Error("Failed to unmarshal services.json", "error", err)
		return
	}

	for name, svc := range services {
		var handler compute.ServiceHandler
		switch svc.Type {
		case "script", "exec":
			handler = compute.BuildScriptHandler(svc.Exec)
		case "grpc":
			handler = compute.BuildGRPCHandler(svc.Exec, 10*time.Second)
		default:
			s.Config.Logger.Warn("Unknown service type", "type", svc.Type, "service", name)
			continue
		}

		if err := s.Compute.RegisterNewService(svc.Schema, handler); err != nil {
			s.Config.Logger.Error("Failed to register local service", "service", name, "error", err)
		} else {
			s.Config.Logger.Info("Local service registered", "service", name, "type", svc.Type)
		}
	}
}

// Delegated methods for backward compatibility & Continuous Granularity.

func (s *Server) SetPeerOnline(peerID string, online bool) {
	s.Peers.SetPeerOnline(peerID, online)
}

func (s *Server) IsPeerOnline(peerID string) bool {
	return s.Peers.IsPeerOnline(peerID)
}

func (s *Server) RemovePeer(peerID string) {
	s.Peers.RemovePeer(peerID)
}

func (s *Server) announceLeave(ctx context.Context) {
	peers := s.GetPeersCopy()
	payload := map[string]string{"id": s.Config.ID}
	for peerID := range peers {
		_ = s.peerClient.Leave(ctx, peerID, payload)
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
		if updater, ok := s.peerClient.(interface {
			UpdatePeerRoute(peerID string, record protocol.AddressRecord)
		}); ok {
			updater.UpdatePeerRoute(peerID, addressRecord)
		}
	}
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	for peerID := range s.GetPeersCopy() {
		payload := protocol.PeerNotification{
			File:   fileInfo,
			Source: s.Config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerID, payload)
		if err != nil {
			s.Config.Logger.Debug("Unreachable peer for real-time notification", "peerID", peerID, "error", err)
			s.SetPeerOnline(peerID, false)
		} else {
			s.SetPeerOnline(peerID, true)
		}
	}
}

func (s *Server) RequestServiceToCluster(query protocol.DiscoveryQuery) (string, protocol.ServiceSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var bids []protocol.ServiceBid
	var mu sync.Mutex
	var wg sync.WaitGroup

	peers := s.GetPeersCopy()
	for peerID := range peers {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			bid, err := s.peerClient.FetchServiceBid(ctx, peerID, query)
			if err != nil {
				s.Config.Logger.Error("FetchServiceBid failed", "peerID", peerID, "err", err)
				s.SetPeerOnline(peerID, false)
			} else {
				s.SetPeerOnline(peerID, true)
			}
			if err != nil || !bid.CanAccept {
				return
			}
			mu.Lock()
			bids = append(bids, bid)
			mu.Unlock()
		}(peerID)
	}

	wg.Wait()

	if len(bids) == 0 {
		return "", protocol.ServiceSchema{}, fmt.Errorf("no nodes available for service '%s'", query.Service)
	}

	bestBid := bids[0]
	if query.SortStrategy == protocol.StrategyFastest {
		for _, bid := range bids {
			if bid.EstimatedMillis < bestBid.EstimatedMillis {
				bestBid = bid
			}
		}
	}

	return bestBid.NodeAddr, bestBid.Schema, nil
}

func (s *Server) DispatchTask(targetPeerID string, req protocol.TaskRequest) error {
	s.Compute.RegisterOutgoingTask(req)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.peerClient.SubmitTask(ctx, targetPeerID, req)
	if err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		s.SetPeerOnline(targetPeerID, false)
		return fmt.Errorf("failed to dispatch task to peer: %v", err)
	}
	s.SetPeerOnline(targetPeerID, true)
	return nil
}

func (s *Server) GetPeersCopy() map[string]string {
	return s.Peers.GetPeersCopy()
}

func (s *Server) ExecuteSync() error {
	for peerID := range s.GetPeersCopy() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		manifest, err := s.peerClient.FetchManifest(ctx, peerID)
		cancel()
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			s.SetPeerOnline(peerID, false)
			continue
		}
		s.SetPeerOnline(peerID, true)
		missingFiles := s.Storage.ProcessRemoteManifest(manifest)
		for _, file := range missingFiles {
			s.downloadQueue <- DownloadJob{
				File:   file,
				Source: peerID,
			}
		}
	}
	return nil
}

func (s *Server) AnnouncePresence(sponsorAddress string) error {
	payload := protocol.AddPeerRequest{
		ID:      s.Config.ID,
		Address: protocol.AddressRecord{Addresses: []string{s.Config.Address}},
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

func (s *Server) downloadWorker() {
	for {
		select {
		case <-s.done:
			return
		case job, ok := <-s.downloadQueue:
			if !ok {
				return
			}
			if job.File.Deleted {
				s.Storage.ProcessRemoteDeletion(job.File)
				continue
			}
			ctxTimeout, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			body, err := s.peerClient.DownloadBlob(ctxTimeout, job.Source, job.File.Hash)
			if err != nil {
				cancel()
				s.Config.Logger.Error("Network error downloading blob", "file", job.File.Name, "error", err)
				continue
			}
			err = s.Storage.StoreRemoteBlob(job.File, body)
			_ = body.Close()
			cancel()
			if err != nil {
				s.Config.Logger.Error("Failed to apply remote blob", "error", err)
			}
		}
	}
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
