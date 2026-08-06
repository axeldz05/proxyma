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
		if bid, canAccept := s.Compute.BuildServiceBid(query); canAccept {
			bid.Schema = schema
			bids = append(bids, bid)
		}
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

	bestBid := selectBestServiceBid(bids, query.SortStrategy)
	return bestBid.NodeID, bestBid.NodeAddr, bestBid.Schema, nil
}

// selectBestServiceBid picks a bid by SortStrategy. Empty strategy defaults to StrategyFastest
// (lowest EstimatedMillis). Ties break by NodeID ascending for determinism.
func selectBestServiceBid(bids []protocol.ServiceBid, strategy string) protocol.ServiceBid {
	best := bids[0]
	bestScore := bidStrategyScore(best, strategy)
	for _, bid := range bids[1:] {
		score := bidStrategyScore(bid, strategy)
		if score < bestScore || (score == bestScore && bid.NodeID < best.NodeID) {
			best = bid
			bestScore = score
		}
	}
	return best
}

func bidStrategyScore(bid protocol.ServiceBid, strategy string) int64 {
	switch strategy {
	case protocol.StrategyCheapest:
		if bid.CostUnits > 0 {
			return bid.CostUnits
		}
		return bid.EstimatedMillis
	case protocol.StrategyLowPower:
		if bid.PowerScore > 0 {
			return bid.PowerScore
		}
		return int64(bid.CPULoad * 1000)
	default: // StrategyFastest or ""
		return bid.EstimatedMillis
	}
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
	s.Storage.StageAndRewrite(req.Payload, false)

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
