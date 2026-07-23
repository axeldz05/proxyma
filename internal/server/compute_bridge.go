package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"strings"
	"sync"
	"time"
)

func (s *Server) RequestServiceToCluster(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
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
				s.SetPeerOffline(peerID, err)
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
		return "", "", protocol.ServiceSchema{}, fmt.Errorf("no nodes available for service '%s'", query.Service)
	}

	bestBid := bids[0]
	if query.SortStrategy == protocol.StrategyFastest {
		for _, bid := range bids {
			if bid.EstimatedMillis < bestBid.EstimatedMillis {
				bestBid = bid
			}
		}
	}

	return bestBid.NodeID, bestBid.NodeAddr, bestBid.Schema, nil
}

func (s *Server) DispatchTask(targetPeerID string, req protocol.TaskRequest) error {
	if req.Payload != nil {
		for k, v := range req.Payload {
			if pathStr, ok := v.(string); ok && pathStr != "" && !strings.HasPrefix(pathStr, "vfs://") {
				if fi, err := os.Stat(pathStr); err == nil && !fi.IsDir() {
					f, err := os.Open(pathStr)
					if err == nil {
						hash, _, err := s.Storage.SavePhysicalBlob(f)
						_ = f.Close()
						if err == nil {
							s.Storage.Upsert(protocol.IndexEntry{
								Name:    filepath.Base(pathStr),
								Hash:    hash,
								Size:    fi.Size(),
								Version: 1,
							})
							req.Payload[k] = "vfs://" + hash
						}
					}
				}
			}
		}
	}

	s.Compute.RegisterOutgoingTask(req)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.peerClient.SubmitTask(ctx, targetPeerID, req)
	if err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		s.SetPeerOffline(targetPeerID, err)
		return err
	}
	s.SetPeerOnline(targetPeerID, true)
	return nil
}

func (s *Server) ensureQUICSession(peerID string) {
	if s.quicMgr == nil {
		return
	}
	record, ok := s.Peers.GetPeerRecord(peerID)
	if !ok {
		return
	}
	hasQuic := false
	for _, addr := range record.Addresses {
		if strings.HasPrefix(addr, "quic://") {
			hasQuic = true
			break
		}
	}
	if !hasQuic {
		return
	}
	if _, sessionExists := s.quicMgr.GetSession(peerID); sessionExists {
		return
	}

	if updater, ok := s.peerClient.(interface {
		UpdatePeerRoute(peerID string, record protocol.AddressRecord)
	}); ok {
		updater.UpdatePeerRoute(peerID, record)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer waitCancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
			if _, sessionExists := s.quicMgr.GetSession(peerID); sessionExists {
				return
			}
		}
	}
}
