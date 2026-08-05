package server

import (
	"context"
	"fmt"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"sort"
)

func (s *Server) ExecuteSync() error {
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCSync}, func(ctx context.Context, peerID string) error {
		s.ensureQUICSession(peerID)
		ctx = context.WithValue(ctx, p2p.BypassHolePunchKey{}, true)
		manifest, err := s.peerClient.FetchManifest(ctx, peerID)
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			return err
		}
		missingFiles := s.Storage.ProcessRemoteManifest(manifest)
		for _, file := range missingFiles {
			s.downloadQueue <- DownloadJob{
				File:   file,
				Source: peerID,
			}
		}
		return nil
	})
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

			ctx, cancel := context.WithTimeout(context.Background(), PeerRPCBlobLong)
			err := s.callPeer(ctx, job.Source, func(ctx context.Context, peerID string) error {
				return s.fetchBlobFromPeer(ctx, peerID, job.File)
			})
			cancel()
			if err != nil {
				s.Config.Logger.Error("Failed to download blob from peer", "hash", job.File.Hash, "peer", job.Source, "error", err)
			} else {
				s.Config.Logger.Info("Successfully downloaded and stored blob", "file", job.File.Name, "hash", job.File.Hash)
			}
		}
	}
}

// fetchBlobFromPeer downloads a blob from peer and stores it locally (L2).
func (s *Server) fetchBlobFromPeer(ctx context.Context, peerID string, entry protocol.IndexEntry) error {
	body, err := s.peerClient.DownloadBlob(ctx, peerID, entry.Hash)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if entry.Name != "" {
		return s.Storage.StoreRemoteBlob(entry, body)
	}
	_, _, err = s.Storage.SavePhysicalBlob(body)
	return err
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
		ctxTimeout, cancel := context.WithTimeout(context.Background(), PeerRPCBlob)
		err := s.fetchBlobFromPeer(ctxTimeout, peerID, entry)
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
	payload := protocol.PeerNotification{
		File:   fileInfo,
		Source: s.Config.Address,
	}
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault}, func(ctx context.Context, peerID string) error {
		err := s.peerClient.Notify(ctx, peerID, payload)
		if err != nil {
			s.Config.Logger.Debug("Unreachable peer for real-time notification", "peerID", peerID, "error", err)
		}
		return err
	})
}
