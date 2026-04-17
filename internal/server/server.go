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
}

func New(cfg protocol.NodeConfig, httpClient *http.Client) *Server {
	peerClient := p2p.NewHTTPPeerClient(httpClient)

	s := &Server{
		Config:     cfg,
		peers:      make(map[string]string),
		peerClient: peerClient,
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Storage = storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers)
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

	if s.Storage != nil {
		s.Storage.Close()
		s.Config.Logger.Info("Storage Engine closed.")
	}
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
	s.peers[peerID] = address
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

    peers := s.GetPeersCopy() //
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
