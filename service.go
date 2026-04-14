package main

import (
	"context"
	"fmt"
	"io"
	"time"
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
)

func (s *Server) SyncStorage() error {
	peers := s.getPeersCopy()
	for _, peerAddress := range peers {
		err := func(pAddr string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			remoteManifest, err := s.peerClient.FetchManifest(ctx, peerAddress)
			if err != nil {
				return err
			}
			for logicalName, remoteFileInfo := range remoteManifest {
				updated := s.vfs.Upsert(remoteFileInfo)
				if updated && !remoteFileInfo.Deleted {
					if _, subscribed := s.subscriptions.Load(logicalName); subscribed {
						s.config.Logger.Debug("DownloadJob added", "file", remoteFileInfo.Name, "source", peerAddress)
						s.downloadQueue <- DownloadJob{
							File:   remoteFileInfo,
							Source: peerAddress,
						}
					}
				}
			}
			return nil
		}(peerAddress)
		if err != nil {
			s.config.Logger.Warn("Failed to synchronize with peer", "peerAddress", peerAddress, "error", err)
		}
	}
	return nil
}

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

func (s *Server) downloadFileFromPeer(fileInfo IndexEntry, sourceAddr string) {
	if fileInfo.Deleted {
		savedFileInfo, exists := s.vfs.Get(fileInfo.Name)
		if s.vfs.Upsert(fileInfo) {
			if exists {
				s.storage.DeleteBlob(savedFileInfo.Hash)
			}
			s.config.Logger.Info("File deleted", "file", fileInfo.Name)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := s.peerClient.DownloadBlob(ctx, sourceAddr, fileInfo.Hash)
	if err != nil {
		s.config.Logger.Error("Failed to download blob", "file", fileInfo.Name, "error", err)
		return
	}
	defer body.Close()

	savedHash, _, err := s.storage.SaveBlob(body)
	if err != nil {
		s.config.Logger.Error("Failed to save blob", "file", fileInfo.Name, "error", err)
		return
	}
	if savedHash != fileInfo.Hash {
		s.config.Logger.Warn("SECURITY ALERT: Peer has sent corrupted or false hash", "expected", fileInfo.Hash, "got", savedHash)
		return
	}

	entry, exists := s.vfs.Get(fileInfo.Name)
	if exists && entry.Version == fileInfo.Version && !entry.Deleted {
		s.config.Logger.Debug("Successfully downloaded and applied file", "file",  fileInfo.Name)
	} else {
		s.config.Logger.Debug("Download discarded due to obsolescence or deletion while downloading", "file", fileInfo.Name, 
			"remote file version", fileInfo.Version, "current local version", entry.Version)
		s.storage.DeleteBlob(fileInfo.Hash)
	}
}

func (s *Server) DeleteLocalFile(fileName string) error {
	entry, exists := s.vfs.Get(fileName)
	if !exists {
		return fmt.Errorf("file %s not found", fileName)
	}
	fileMeta := IndexEntry{
		Name:    entry.Name,
		Size:    entry.Size,
		Hash:    entry.Hash,
		Version: entry.Version + 1,
		Deleted: true,
	}
	if s.vfs.Upsert(fileMeta) {
		s.storage.DeleteBlob(entry.Hash)
		go s.notifyPeers(fileMeta)
	}
	return nil
}

func (s *Server) SaveLocalFile(fileName string, content io.Reader) error {
	hash, fileSize, err := s.storage.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("Error saving the blob %s: %v", fileName, err.Error())
	}

	newVersion := 1
	if existingMeta, exists := s.vfs.Get(fileName); exists {
		newVersion = existingMeta.Version + 1
	}
	fileMeta := IndexEntry{
		Name:    fileName,
		Size:    fileSize,
		Hash:    hash,
		Version: newVersion,
	}
	s.vfs.Upsert(fileMeta)

	s.subscriptions.Store(fileName, true)

	go s.notifyPeers(fileMeta)

	return nil
}

func (s *Server) downloadWorker() {
	for job := range s.downloadQueue {
		s.downloadFileFromPeer(job.File, job.Source)
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
