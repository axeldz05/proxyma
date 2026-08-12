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
		if bid.CanAccept && protocol.SupportsCapabilities(bid.Capabilities, query.RequiredCapabilities) {
			bids = append(bids, bid)
		}
	}

	if len(bids) == 0 {
		return "", "", protocol.ServiceSchema{}, fmt.Errorf("no nodes available for service '%s'", query.Service)
	}

	bestBid := selectBestServiceBid(bids, protocol.NormalizeSortStrategy(query.SortStrategy))
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

func (s *Server) submitTrackedTask(req protocol.TaskRequest, submit func(protocol.TaskRequest) error) error {
	if err := s.Compute.PreparePipelineTargets(&req); err != nil {
		return fmt.Errorf("prepare pipeline targets: %w", err)
	}
	s.Compute.RegisterOutgoingTask(req)
	if err := submit(req); err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		return err
	}
	return nil
}

func (s *Server) DispatchTask(targetPeerID string, req protocol.TaskRequest) error {
	if req.RequesterNodeID == "" {
		req.RequesterNodeID = s.Config.ID
	}
	req.ExpectedProducerNodeID = targetPeerID
	if err := s.Compute.BindPipelineTask(&req); err != nil {
		return fmt.Errorf("bind pipeline task: %w", err)
	}
	if err := s.Compute.BindPipelineStepTarget(&req, targetPeerID); err != nil {
		return fmt.Errorf("bind pipeline step target: %w", err)
	}
	if err := s.Storage.StageAndRewrite(req.Payload, false); err != nil {
		return fmt.Errorf("stage payload for dispatch: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()
	if req.PipelineState != nil && req.PipelineState.CurrentStep > 0 {
		schema, exists := s.Compute.GetPipeline(req.Service)
		if !exists || req.PipelineState.CurrentStep >= len(schema.Steps) {
			return fmt.Errorf("cannot validate pipeline continuation capability for step %d", req.PipelineState.CurrentStep)
		}
		query := protocol.DiscoveryQuery{
			Service: schema.Steps[req.PipelineState.CurrentStep].Service,
			RequiredCapabilities: map[string]int{
				protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
			},
		}
		bid, err := s.peerClient.FetchServiceBid(ctx, targetPeerID, query)
		if err != nil {
			return fmt.Errorf("verify pipeline continuation capability: %w", err)
		}
		if !bid.CanAccept || !protocol.SupportsCapabilities(bid.Capabilities, query.RequiredCapabilities) {
			return fmt.Errorf(
				"peer %q does not support pipeline state capability v%d",
				targetPeerID,
				protocol.PipelineStateCapabilityVersion,
			)
		}
		// Capability claims are trusted only because callbacks and dispatch are
		// restricted to enrolled mTLS peers; this is not cryptographic provenance.
	}

	submit := func(prepared protocol.TaskRequest) error {
		return s.callPeer(ctx, targetPeerID, func(ctx context.Context, peerID string) error {
			return s.peerClient.SubmitTask(ctx, peerID, prepared)
		})
	}
	if req.RequesterNodeID != s.Config.ID {
		return submit(req)
	}
	return s.submitTrackedTask(req, submit)
}

func (s *Server) ensureQUICSession(peerID string) {
	natState := s.CurrentNATState()
	qm := natState.QUICManager
	if qm == nil {
		return
	}
	record, ok := s.Peers.GetPeerRecord(peerID)
	if !ok {
		return
	}
	if _, ok := p2p.FirstQUICAddr(record.Addresses); !ok {
		return
	}
	if _, sessionExists := qm.GetSession(peerID); sessionExists {
		return
	}

	s.peerClient.UpdatePeerRoute(peerID, record)

	waitCtx, waitCancel := context.WithTimeout(s.lifetimeCtx, PeerRPCQUICWait)
	defer waitCancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
			if _, sessionExists := qm.GetSession(peerID); sessionExists {
				return
			}
		}
	}
}
