package main

import (
	"context"
	"fmt"
	"time"
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
)

func (s *Server) AddPeer(peerID, address string) {
	s.Peers[peerID] = address
}

func (s *Server) notifyPeers(fileInfo IndexEntry) {
	peers := make(map[string]string, len(s.Peers))
	for k, v := range s.Peers {
		peers[k] = v
	}

	for peerID, peerAddr := range peers {
		if peerID == s.config.ID {
			continue
		}
		payload := PeerNotification{
			File:   fileInfo,
			Source: s.config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerAddr, payload)
		if err != nil {
			s.config.Logger.Error("Error notifying peer", "peerID", peerID, "error", err)
		}
	}
}

func (s *Server) RequestServiceToCluster(query DiscoveryQuery) (string, ServiceSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var bids []ServiceBid
	var mu sync.Mutex
	var wg sync.WaitGroup

	queryJSON, _ := json.Marshal(query)

	peers := s.getPeersCopy() 
	for _, peerAddr := range peers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()

			req, err := http.NewRequestWithContext(ctx, "POST", addr+"/services/bid", bytes.NewReader(queryJSON))
			if err != nil { return }
			req.Header.Set("Content-Type", "application/json")

			resp, err := s.peerClient.(*HTTPPeerClient).client.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK { return }
			defer resp.Body.Close()

			var bid ServiceBid
			if err := json.NewDecoder(resp.Body).Decode(&bid); err == nil && bid.CanAccept {
				mu.Lock()
				bids = append(bids, bid)
				mu.Unlock()
			}
		}(peerAddr)
	}

	wg.Wait()

	if len(bids) == 0 {
		return "", ServiceSchema{}, fmt.Errorf("no nodes available for service '%s' fulfilling requirements", query.Service)
	}

	bestBid := bids[0]
	
	if query.SortStrategy == StrategyFastest {
		for _, bid := range bids {
			if bid.EstimatedMillis < bestBid.EstimatedMillis {
				bestBid = bid
			}
		}
	}

	return bestBid.NodeAddr, bestBid.Schema, nil
}


func (s *Server) DispatchTask(targetPeerAddr string, req TaskRequest) error {
	s.compute.taskStatuses.Store(req.TaskID, ServiceTaskResponse{
		TaskID:  req.TaskID,
		Service: req.Service,
		Status:  "pending",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.peerClient.SubmitTask(ctx, targetPeerAddr, req)
	if err != nil {
		s.compute.taskStatuses.Store(req.TaskID, ServiceTaskResponse{
			TaskID:  req.TaskID,
			Service: req.Service,
			Status:  "failed",
			Error:   err.Error(),
		})
		return fmt.Errorf("failed to dispatch task to peer: %v", err)
	}
	return nil
}
