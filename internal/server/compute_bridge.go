package server

import (
	"context"
	"fmt"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"strings"
	"sync"
	"time"
)

func (s *Server) RequestServiceToCluster(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCShort)
	defer cancel()

	var bids []protocol.ServiceBid
	var mu sync.Mutex
	var wg sync.WaitGroup

	if schema, ok := s.Compute.GetService(query.Service); ok {
		bids = append(bids, protocol.ServiceBid{
			NodeID:          s.Config.ID,
			NodeAddr:        s.Config.Address,
			Schema:          schema,
			CanAccept:       true,
			EstimatedMillis: 10,
		})
	}

	peers := s.GetPeersCopy()
	for peerID := range peers {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			var bid protocol.ServiceBid
			err := s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
				var bidErr error
				bid, bidErr = s.peerClient.FetchServiceBid(ctx, peerID, query)
				return bidErr
			})
			if err != nil {
				s.Config.Logger.Error("FetchServiceBid failed", "peerID", peerID, "err", err)
				return
			}
			if !bid.CanAccept {
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
					hash, _, err := s.Storage.StageLocalFile(pathStr)
					if err == nil {
						req.Payload[k] = "vfs://" + hash
					}
				}
			}
		}
	}

	s.Compute.RegisterOutgoingTask(req)

	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()

	err := s.callPeer(ctx, targetPeerID, func(ctx context.Context, peerID string) error {
		return s.peerClient.SubmitTask(ctx, peerID, req)
	})
	if err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		return err
	}
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
	if _, ok := p2p.FirstQUICAddr(record.Addresses); !ok {
		return
	}
	if _, sessionExists := s.quicMgr.GetSession(peerID); sessionExists {
		return
	}

	s.peerClient.UpdatePeerRoute(peerID, record)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), PeerRPCQUICWait)
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
