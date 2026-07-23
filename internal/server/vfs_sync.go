package server

import (
	"context"
	"fmt"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"sort"
	"time"
)

func (s *Server) ExecuteSync() error {
	for peerID := range s.GetPeersCopy() {
		s.ensureQUICSession(peerID)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ctx = context.WithValue(ctx, p2p.BypassHolePunchKey{}, true)
		manifest, err := s.peerClient.FetchManifest(ctx, peerID)
		cancel()
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			s.SetPeerOffline(peerID, err)
			continue
		}
		s.SetPeerOnline(peerID, true)
		missingFiles := s.Storage.ProcessRemoteManifest(manifest)
		for _, file := range missingFiles {
			s.downloadQueue <- DownloadJob{
				File:   file,
				Source: peerID,
			}
		}
	}
	return nil
}

func (s *Server) downloadWorker() {
	for {
		select {
		case <-s.done:
			return
		case job, ok := <-s.downloadQueue:
			if !ok {
				return
			}
			if job.File.Deleted {
				s.Storage.ProcessRemoteDeletion(job.File)
				continue
			}
			s.ensureQUICSession(job.Source)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			body, err := s.peerClient.DownloadBlob(ctx, job.Source, job.File.Hash)
			if err != nil {
				s.Config.Logger.Error("Failed to download blob from peer", "hash", job.File.Hash, "peer", job.Source, "error", err)
				s.SetPeerOffline(job.Source, err)
				cancel()
				continue
			}
			s.SetPeerOnline(job.Source, true)
			err = s.Storage.StoreRemoteBlob(job.File, body)
			_ = body.Close()
			cancel()
			if err != nil {
				s.Config.Logger.Error("Failed to store remote blob", "hash", job.File.Hash, "error", err)
			} else {
				s.Config.Logger.Info("Successfully downloaded and stored blob", "file", job.File.Name, "hash", job.File.Hash)
			}
		}
	}
}

func (s *Server) FetchFileOnDemand(name string) error {
	entry, ok := s.Storage.GetFileMeta(name)
	if !ok {
		return fmt.Errorf("file '%s' not found in VFS metadata", name)
	}
	hasLocal, _ := s.Storage.HasPhysicalBlob(entry.Hash)
	if hasLocal {
		return nil
	}

	peersSnapshot := s.GetPeersRecordCopy()
	var downloadErr error
	for peerID, addrRec := range peersSnapshot {
		if peerID == s.Config.ID || len(addrRec.Addresses) == 0 {
			continue
		}
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		body, err := s.peerClient.DownloadBlob(ctxTimeout, peerID, entry.Hash)
		if err != nil {
			cancel()
			downloadErr = err
			continue
		}
		err = s.Storage.StoreRemoteBlob(entry, body)
		_ = body.Close()
		cancel()
		if err == nil {
			s.Config.Logger.Info("Successfully fetched unsubscribed blob on demand into cache", "file", name, "hash", entry.Hash)
			return nil
		}
		downloadErr = err
	}

	if downloadErr != nil {
		return fmt.Errorf("failed to fetch file '%s' from cluster peers: %v", name, downloadErr)
	}
	return fmt.Errorf("no peer holds physical replica for file '%s'", name)
}

func (s *Server) LocalVFSList() []protocol.VFSFileStatus {
	snapshot := s.Storage.GetVFSSnapshot()
	upSpeed, downSpeed := s.GetCurrentBandwidth()

	var list []protocol.VFSFileStatus
	for name, entry := range snapshot {
		hasLocal, _ := s.Storage.HasPhysicalBlob(entry.Hash)
		isSubscribed := s.Storage.IsSubscribed(name)

		var sentSpeed, recvSpeed float64
		if hasLocal {
			sentSpeed, recvSpeed = s.GetCategoryBandwidth("vfs:" + entry.Hash)
			if sentSpeed == 0 && recvSpeed == 0 {
				sentSpeed = upSpeed
				recvSpeed = downSpeed
			}
		}

		list = append(list, protocol.VFSFileStatus{
			Name:        name,
			Size:        entry.Size,
			Hash:        entry.Hash,
			Version:     entry.Version,
			Deleted:     entry.Deleted,
			HasLocal:    hasLocal,
			Subscribed:  isSubscribed,
			UpSpeed:    sentSpeed,
			DownSpeed:  recvSpeed,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	for peerID := range s.GetPeersCopy() {
		payload := protocol.PeerNotification{
			File:   fileInfo,
			Source: s.Config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerID, payload)
		if err != nil {
			s.Config.Logger.Debug("Unreachable peer for real-time notification", "peerID", peerID, "error", err)
			s.SetPeerOffline(peerID, err)
		} else {
			s.SetPeerOnline(peerID, true)
		}
	}
}
