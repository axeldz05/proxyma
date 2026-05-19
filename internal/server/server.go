package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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
	Config         protocol.NodeConfig
	Compute        *compute.ComputeEngine
	Storage        *storage.StorageEngine
	peers          map[string]string
	peerClient     p2p.PeerClient
	httpServer     *http.Server
	downloadQueue  chan DownloadJob
	peersMu        sync.RWMutex
	clusterServices   map[string]map[string]protocol.ServiceSchema
	clusterServicesMu sync.RWMutex
	inviteMu       sync.Mutex
	pendingInvites map[string]time.Time
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) *Server {
	s := &Server{
		Config:         cfg,
		peers:          make(map[string]string),
		peerClient:     peerClient,
		downloadQueue:  make(chan DownloadJob, 100),
		clusterServices: make(map[string]map[string]protocol.ServiceSchema),
		pendingInvites: make(map[string]time.Time),
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Storage = storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers, func(file protocol.IndexEntry, rawSource string) error {
		for _, peerAddress := range s.GetPeersCopy() {
			if rawSource == peerAddress {
				s.downloadQueue <- DownloadJob{
					File:   file,
					Source: peerAddress,
				}
				return nil
			}
		}
		return fmt.Errorf("peer of address %s not found", rawSource)
	})

	for range cfg.Workers {
		go s.downloadWorker(context.Background())
	}
	go s.inviteSweeper(context.Background())
	return s
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	mux := s.MountHandlers()
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

	s.Config.Logger.Info("Node shutdown complete.")
	return nil
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
		if svc.Type == "script" || svc.Type == "exec" {
			handler = compute.BuildScriptHandler(svc.Exec)
		} else if svc.Type == "grpc" {
			// dummy grpc handler builder if none, but let's assume BuildGRPCHandler exists
			handler = compute.BuildGRPCHandler(svc.Exec, 10*time.Second) 
		} else {
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

func (s *Server) AddPeer(peerID, address string) {
	s.peersMu.Lock()
	s.peers[peerID] = address
	s.peersMu.Unlock()
	s.Config.Logger.Info("peerID added to peers", "peerID", peerID, "node", s.Config.ID)
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	for peerID, peerAddr := range s.GetPeersCopy() {
		payload := protocol.PeerNotification{
			File:   fileInfo,
			Source: s.Config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerAddr, payload)
		if err != nil {
			// it's assumed that, if the peer reconnects to the cluster, it automatically
			// executes a sync.
			s.Config.Logger.Debug("Unreachable peer for real-time notification", "peerID", peerID, "error", err)
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
	for _, peerAddr := range peers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			bid, err := s.peerClient.FetchServiceBid(ctx, addr, query)
			if err != nil || !bid.CanAccept {
				return
			}
			mu.Lock()
			bids = append(bids, bid)
			mu.Unlock()
		}(peerAddr)
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

func (s *Server) DispatchTask(targetPeerAddr string, req protocol.TaskRequest) error {
	s.Compute.RegisterOutgoingTask(req)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.peerClient.SubmitTask(ctx, targetPeerAddr, req)
	if err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		return fmt.Errorf("failed to dispatch task to peer: %v", err)
	}
	return nil
}

func (s *Server) GetPeersCopy() map[string]string {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	peers := make(map[string]string, len(s.peers))
	maps.Copy(peers, s.peers)
	return peers
}

func (s *Server) ExecuteSync() error {
	for peerID, peerAddress := range s.GetPeersCopy() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		manifest, err := s.peerClient.FetchManifest(ctx, peerAddress)
		cancel()
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			continue
		}
		missingFiles := s.Storage.ProcessRemoteManifest(manifest)
		for _, file := range missingFiles {
			s.downloadQueue <- DownloadJob{
				File:   file,
				Source: peerAddress,
			}
		}
	}
	return nil
}

func (s *Server) AnnouncePresence(sponsorAddress string) error {
	payload := protocol.AddPeerRequest{
		ID:      s.Config.ID,
		Address: s.Config.Address,
	}

	announceResp, err := s.peerClient.Announce(sponsorAddress, payload)
	if err != nil {
		s.Config.Logger.Error("Error while announcing from sponsor", "sponsor", sponsorAddress, "error", err)
	}
	s.Config.Logger.Info("AnnounceResp received without errors", "resp", announceResp)
	for id, addr := range announceResp {
		if id != s.Config.ID {
			s.AddPeer(id, addr)
		}
	}
	s.Config.Logger.Info("Successfully synced topology from sponsor", "peers_count", len(announceResp))
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
