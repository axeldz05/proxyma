package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Config            protocol.NodeConfig
	Compute           *compute.ComputeEngine
	Storage           *storage.StorageEngine
	handler           http.Handler
	peers             map[string]protocol.AddressRecord
	peerClient        p2p.PeerClient
	httpServer        *http.Server
	downloadQueue     chan DownloadJob
	peersMu           sync.RWMutex
	clusterServices   map[string]map[string]protocol.ServiceSchema
	clusterServicesMu sync.RWMutex
	inviteMu          sync.Mutex
	pendingInvites    map[string]time.Time
	unixListener      net.Listener
	relayQueues       map[string]chan protocol.RelayRequest
	relayWaiters      map[string]chan protocol.RelayResponse
	relayMu           sync.RWMutex
	activePeers       map[string]bool
	activePeersMu     sync.RWMutex
	totalSent         int64
	totalReceived     int64
	sentHistory       []TransferRecord
	receivedHistory   []TransferRecord
	bandwidthMu       sync.RWMutex
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
		Config:          cfg,
		peers:           make(map[string]protocol.AddressRecord),
		peerClient:      peerClient,
		downloadQueue:   make(chan DownloadJob, 100),
		clusterServices: make(map[string]map[string]protocol.ServiceSchema),
		pendingInvites:  make(map[string]time.Time),
		relayQueues:  make(map[string]chan protocol.RelayRequest),
		relayWaiters: make(map[string]chan protocol.RelayResponse),
		activePeers:  make(map[string]bool),
	}

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
		go s.downloadWorker(context.Background())
	}
	go s.inviteSweeper(context.Background())
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
			// dummy grpc handler builder if none, but let's assume BuildGRPCHandler exists
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

func (s *Server) SetPeerOnline(peerID string, online bool) {
	s.activePeersMu.Lock()
	defer s.activePeersMu.Unlock()
	s.activePeers[peerID] = online
}

func (s *Server) IsPeerOnline(peerID string) bool {
	s.activePeersMu.RLock()
	defer s.activePeersMu.RUnlock()
	return s.activePeers[peerID]
}

func (s *Server) RemovePeer(peerID string) {
	s.peersMu.Lock()
	delete(s.peers, peerID)
	s.peersMu.Unlock()

	s.activePeersMu.Lock()
	delete(s.activePeers, peerID)
	s.activePeersMu.Unlock()

	s.clusterServicesMu.Lock()
	delete(s.clusterServices, peerID)
	s.clusterServicesMu.Unlock()

	s.Config.Logger.Info("peerID removed from peers", "peerID", peerID)
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
	s.clusterServicesMu.RLock()
	defer s.clusterServicesMu.RUnlock()
	services := make(map[string]protocol.ServiceSchema)
	if peerServices, ok := s.clusterServices[peerID]; ok {
		maps.Copy(services, peerServices)
	}
	return services
}

func (s *Server) SetAddress(addr string) {
	s.Config.Address = addr
	s.Compute.SetAddress(addr)
}

func (s *Server) AddPeer(peerID string, addressRecord protocol.AddressRecord) {
	s.peersMu.Lock()
	defer s.peersMu.Unlock()

	existing, exists := s.peers[peerID]
	if exists {
		if addressRecord.Sequence < existing.Sequence {
			s.Config.Logger.Debug("Ignoring older peer address record", "peerID", peerID, "currentSeq", existing.Sequence, "newSeq", addressRecord.Sequence)
			return
		}
		if addressRecord.Sequence == existing.Sequence {
			addrSet := make(map[string]bool)
			for _, a := range existing.Addresses {
				addrSet[a] = true
			}
			for _, a := range addressRecord.Addresses {
				addrSet[a] = true
			}
			var newAddrs []string
			for a := range addrSet {
				newAddrs = append(newAddrs, a)
			}
			addressRecord.Addresses = newAddrs
		}
	}

	s.peers[peerID] = addressRecord
	s.SetPeerOnline(peerID, true)
	s.Config.Logger.Info("peerID added to peers", "peerID", peerID, "node", s.Config.ID)

	if updater, ok := s.peerClient.(interface {
		UpdatePeerRoute(peerID string, record protocol.AddressRecord)
	}); ok {
		updater.UpdatePeerRoute(peerID, addressRecord)
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
			// it's assumed that, if the peer reconnects to the cluster, it automatically
			// executes a sync.
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
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	peers := make(map[string]string, len(s.peers))
	for k, v := range s.peers {
		if len(v.Addresses) > 0 {
			peers[k] = v.Addresses[0] // Return primary address for backward compatibility right now
		}
	}
	return peers
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

func (s *Server) downloadWorker(ctx context.Context) {
	for job := range s.downloadQueue {
		if job.File.Deleted {
			s.Storage.ProcessRemoteDeletion(job.File)
			continue
		}
		ctxTimeout, cancel := context.WithTimeout(ctx, 2*time.Minute)
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

func (s *Server) inviteSweeper(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			s.inviteMu.Lock()
			for secret, expiration := range s.pendingInvites {
				if now.After(expiration) {
					delete(s.pendingInvites, secret)
					s.Config.Logger.Debug("Expired invite removed from memory")
				}
			}
			s.inviteMu.Unlock()
		}
	}
}

func (s *Server) AddPendingInvite(secret string, expiration time.Time) {
	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()
	s.pendingInvites[secret] = expiration
}

func (s *Server) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return s.peerClient.DiscoverServices(ctx, peerID)
}


func (s *Server) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	record, exists := s.peers[peerID]
	return record, exists
}

func (s *Server) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	snapshot := make(map[string]protocol.AddressRecord, len(s.peers))
	maps.Copy(snapshot, s.peers)
	return snapshot
}

type TransferRecord struct {
	Timestamp time.Time
	Bytes     int64
	Category  string
}

func (s *Server) RecordBytesSent(n int64, path string) {
	s.bandwidthMu.Lock()
	defer s.bandwidthMu.Unlock()
	s.totalSent += n
	s.sentHistory = append(s.sentHistory, TransferRecord{
		Timestamp: time.Now(),
		Bytes:     n,
		Category:  categorizePath(path),
	})
}

func (s *Server) RecordBytesReceived(n int64, path string) {
	s.bandwidthMu.Lock()
	defer s.bandwidthMu.Unlock()
	s.totalReceived += n
	s.receivedHistory = append(s.receivedHistory, TransferRecord{
		Timestamp: time.Now(),
		Bytes:     n,
		Category:  categorizePath(path),
	})
}

func categorizePath(path string) string {
	if strings.HasPrefix(path, "/download/") {
		cleanPath := path
		if idx := strings.Index(path, "?"); idx != -1 {
			cleanPath = path[:idx]
		}
		parts := strings.Split(cleanPath, "/")
		if len(parts) >= 3 {
			hash := parts[2]
			return "vfs:" + hash
		}
		return "vfs:download"
	}
	if strings.HasPrefix(path, "/upload") {
		return "vfs:upload"
	}
	if strings.HasPrefix(path, "/services/") {
		cleanPath := strings.TrimPrefix(path, "/services/")
		if idx := strings.Index(cleanPath, "?"); idx != -1 {
			queryParams := cleanPath[idx+1:]
			basePath := cleanPath[:idx]
			for _, param := range strings.Split(queryParams, "&") {
				parts := strings.SplitN(param, "=", 2)
				if len(parts) == 2 && (parts[0] == "service" || parts[0] == "name") {
					return "service:" + parts[1]
				}
			}
			return "service:" + basePath
		}
		return "service:" + cleanPath
	}
	return "other"
}

func (s *Server) GetCurrentBandwidth() (float64, float64) {
	s.bandwidthMu.Lock()
	defer s.bandwidthMu.Unlock()

	now := time.Now()
	threshold := now.Add(-5 * time.Second)

	var sentLast5s int64
	var i int
	for i = len(s.sentHistory) - 1; i >= 0; i-- {
		rec := s.sentHistory[i]
		if rec.Timestamp.Before(threshold) {
			break
		}
		sentLast5s += rec.Bytes
	}
	if i > 0 {
		s.sentHistory = s.sentHistory[i:]
	}

	var recvLast5s int64
	for i = len(s.receivedHistory) - 1; i >= 0; i-- {
		rec := s.receivedHistory[i]
		if rec.Timestamp.Before(threshold) {
			break
		}
		recvLast5s += rec.Bytes
	}
	if i > 0 {
		s.receivedHistory = s.receivedHistory[i:]
	}

	return float64(sentLast5s) / 5.0, float64(recvLast5s) / 5.0
}

func (s *Server) GetCategoryBandwidth(category string) (float64, float64) {
	s.bandwidthMu.RLock()
	defer s.bandwidthMu.RUnlock()

	threshold := time.Now().Add(-5 * time.Second)
	var sent int64
	for _, rec := range s.sentHistory {
		if rec.Timestamp.After(threshold) && rec.Category == category {
			sent += rec.Bytes
		}
	}
	var recv int64
	for _, rec := range s.receivedHistory {
		if rec.Timestamp.After(threshold) && rec.Category == category {
			recv += rec.Bytes
		}
	}
	return float64(sent) / 5.0, float64(recv) / 5.0
}

func (s *Server) GetTotalBandwidth() (int64, int64) {
	s.bandwidthMu.RLock()
	defer s.bandwidthMu.RUnlock()
	return s.totalSent, s.totalReceived
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
			r.Body = &countingReadCloser{
				ReadCloser: r.Body,
				onRead: func(n int) {
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

type countingReadCloser struct {
	io.ReadCloser
	onRead func(int)
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if c.onRead != nil && n > 0 {
		c.onRead(n)
	}
	return n, err
}
