package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Config			protocol.NodeConfig
	Compute 		*compute.ComputeEngine
	Storage 		*storage.StorageEngine
	peers   		map[string]string
	peerClient  	p2p.PeerClient
	httpServer 		*http.Server
	downloadQueue 	chan DownloadJob
	inviteMu        sync.RWMutex
	pendingInvites  map[string]time.Time
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}


func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) *Server {
	s := &Server{
		Config:     	cfg,
		peers:      	make(map[string]string),
		peerClient: 	peerClient,
		downloadQueue: 	make(chan DownloadJob, 100),
		pendingInvites: make(map[string]time.Time),
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Storage = storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers, func(file protocol.IndexEntry, rawSource string) error {
		for _, peerAddress := range s.peers {
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

func (s *Server) SetAddress(addr string) {
	s.Config.Address = addr
	s.Compute.SetAddress(addr)
}

func (s *Server) AddPeer(peerID, address string) {
	s.Config.Logger.Info("peerID added to peers", "peerID", peerID, "node", s.Config.ID)
	s.inviteMu.Lock()
	s.peers[peerID] = address
	s.inviteMu.Unlock()
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	peers := make(map[string]string, len(s.peers))
	maps.Copy(peers, s.peers)

	for peerID, peerAddr := range peers {
		if peerID == s.Config.ID {
			continue
		}
		payload := protocol.PeerNotification{
			File:   fileInfo,
			Source: s.Config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerAddr, payload)
		if err != nil {
			s.Config.Logger.Error("Error notifying peer", "peerID", peerID, "error", err)
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
        s.Compute.MarkTaskAsFailed(req.TaskID, req.Service, err.Error())
        return fmt.Errorf("failed to dispatch task to peer: %v", err)
    }
    return nil
}

func (s *Server) GetPeersCopy() map[string]string{
	peers := make(map[string]string, len(s.peers))
	maps.Copy(peers, s.peers)
	return peers
}

func (srv *Server) ExecuteSync() error {
	for peerID, peerAddress := range srv.peers{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		manifest, err := srv.peerClient.FetchManifest(ctx, peerAddress) 
		cancel()
		if err != nil {
			srv.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			continue 
		}
		missingFiles := srv.Storage.ProcessRemoteManifest(manifest) 
		for _, file := range missingFiles {
			srv.downloadQueue <- DownloadJob{
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

func (srv *Server) downloadWorker(ctx context.Context) {
	for job := range srv.downloadQueue{
		if job.File.Deleted {
			srv.Storage.ProcessRemoteDeletion(job.File)
			continue
		}
		ctxTimeout, cancel := context.WithTimeout(ctx, 2*time.Minute)
		body, err := srv.peerClient.DownloadBlob(ctxTimeout, job.Source, job.File.Hash)
		if err != nil {
			cancel()
			srv.Config.Logger.Error("Network error downloading blob", "file", job.File.Name, "error", err)
			continue
		}
		err = srv.Storage.StoreRemoteBlob(job.File, body)
		_ = body.Close()
		cancel()
		if err != nil {
			srv.Config.Logger.Error("Failed to apply remote blob", "error", err)
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
