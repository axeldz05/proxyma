package server

import (
	"context"
	"fmt"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"time"
)

func (s *Server) RequestServiceToCluster(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
	var bids []protocol.ServiceBid

	if schema, ok := s.Compute.GetService(query.Service); ok {
		bids = append(bids, protocol.ServiceBid{
			NodeID:          s.Config.ID,
			NodeAddr:        s.Config.Address,
			Schema:          schema,
			CanAccept:       true,
			EstimatedMillis: 10,
		})
	}

	peerBids := mapEachPeer(s, forEachPeerOpts{Timeout: PeerRPCShort, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) (protocol.ServiceBid, error) {
		bid, err := s.peerClient.FetchServiceBid(ctx, peerID, query)
		if err != nil {
			s.Config.Logger.Error("FetchServiceBid failed", "peerID", peerID, "err", err)
			return bid, err
		}
		return bid, nil
	})
	for _, bid := range peerBids {
		if bid.CanAccept {
			bids = append(bids, bid)
		}
	}

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

func (s *Server) submitTrackedTask(req protocol.TaskRequest, submit func() error) error {
	s.Compute.RegisterOutgoingTask(req)
	if err := submit(); err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		return err
	}
	return nil
}

func (s *Server) DispatchTask(targetPeerID string, req protocol.TaskRequest) error {
	protocol.RewriteLocalFilePaths(req.Payload, s.Storage.StageLocalFile, false)

	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()

	return s.submitTrackedTask(req, func() error {
		return s.callPeer(ctx, targetPeerID, func(ctx context.Context, peerID string) error {
			return s.peerClient.SubmitTask(ctx, peerID, req)
		})
	})
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
