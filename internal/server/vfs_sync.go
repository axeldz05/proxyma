package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
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

		// Push local entries the peer lacks (or has older). Critical after partition
		// heal: the isolated node can reach the sponsor, but the sponsor often cannot
		// dial the reconnected peer (DNS lag / stale IP) and has no relay of its own.
		for name, local := range s.Storage.GetVFSSnapshot() {
			remote, ok := manifest[name]
			if ok && !local.Deleted && remote.Hash == local.Hash && remote.Version >= local.Version {
				continue
			}
			if ok && local.Deleted && remote.Deleted && remote.Version >= local.Version {
				continue
			}
			nErr := s.peerClient.Notify(ctx, peerID, protocol.PeerNotification{
				File:   local,
				Source: s.Config.Address,
			})
			if nErr != nil {
				s.Config.Logger.Debug("Sync push notify failed", "peer", peerID, "file", name, "error", nErr)
				continue
			}
		}

		for _, file := range missingFiles {
			if err := s.enqueueDownload(DownloadJob{File: file, Source: peerID}); err != nil {
				s.Config.Logger.Warn("Download enqueue skipped", "peer", peerID, "file", file.Name, "error", err)
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
			// Manifest source may only hold metadata (e.g. relay sponsor). Fall back
			// to any reachable peer that still has the physical blob — skip source so
			// callPeer does not re-mark it offline via a synthetic error.
			if err != nil && !errors.Is(err, storage.ErrBlobDiscarded) {
				for peerID := range s.GetPeersCopy() {
					if peerID == s.Config.ID || peerID == job.Source {
						continue
					}
					fbCtx, fbCancel := context.WithTimeout(context.Background(), PeerRPCBlobLong)
					fbErr := s.callPeer(fbCtx, peerID, func(ctx context.Context, peerID string) error {
						return s.fetchBlobFromPeer(ctx, peerID, job.File)
					})
					fbCancel()
					if fbErr == nil {
						err = nil
						break
					}
					if errors.Is(fbErr, storage.ErrBlobDiscarded) {
						err = fbErr
						break
					}
				}
			}
			if errors.Is(err, storage.ErrBlobDiscarded) {
				s.Config.Logger.Debug("Blob download discarded due to obsolescence or deletion", "file", job.File.Name, "hash", job.File.Hash)
			} else if err != nil {
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
	return s.Storage.SaveVerifiedPhysicalBlob(entry.Hash, body)
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

	_, ok = firstPeer(s, forEachPeerOpts{Timeout: PeerRPCBlob, SkipSelf: true}, func(ctx context.Context, peerID string) (struct{}, error) {
		return struct{}{}, s.fetchBlobFromPeer(ctx, peerID, entry)
	})
	if ok {
		s.Config.Logger.Info("Successfully fetched unsubscribed blob on demand into cache", "file", name, "hash", entry.Hash)
		return nil
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
			Name:       name,
			Size:       entry.Size,
			Hash:       entry.Hash,
			Version:    entry.Version,
			Deleted:    entry.Deleted,
			HasLocal:   hasLocal,
			Subscribed: isSubscribed,
			UpSpeed:    sentSpeed,
			DownSpeed:  recvSpeed,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// announceAndSync announces to bootstrap (if set) then runs ExecuteSync.
func (s *Server) announceAndSync() error {
	if s.Config.BootstrapNode != "" {
		_ = s.AnnouncePresence(s.Config.BootstrapNode)
	}
	return s.ExecuteSync()
}

// LocalVFSUpload opens path and saves into VFS under name (SSOT for bind+unix).
func (s *Server) LocalVFSUpload(name, filePath string) error {
	if filePath == "" || name == "" {
		return fmt.Errorf("missing path or name parameter")
	}
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return s.Storage.SaveLocalFile(name, f)
}

// LocalVFSSubscribe sets subscription and optionally kicks announce+sync in background.
func (s *Server) LocalVFSSubscribe(name string, subscribe bool) error {
	if name == "" {
		return protocol.MissingParamError("name")
	}
	s.Storage.SetSubscription(name, subscribe)
	if subscribe {
		go func() { _ = s.announceAndSync() }()
	}
	return nil
}

// LocalLogs returns a copy of the in-memory log buffer.
func (s *Server) LocalLogs() []protocol.LogRecord {
	protocol.LogBufferMu.RLock()
	defer protocol.LogBufferMu.RUnlock()
	if protocol.LogBuffer == nil {
		return []protocol.LogRecord{}
	}
	out := make([]protocol.LogRecord, len(protocol.LogBuffer))
	copy(out, protocol.LogBuffer)
	return out
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	payload := protocol.PeerNotification{
		File:   fileInfo,
		Source: s.Config.Address,
	}
	dedupe := fmt.Sprintf("%s|%s|%d", fileInfo.Name, fileInfo.Hash, fileInfo.Version)
	if fileInfo.Deleted {
		dedupe += "|del"
	}
	s.gossipAll(func(ctx context.Context, peerID string) error {
		return s.notifyWithOutbox(ctx, peerID, kindVFS, dedupe, payload, func(ctx context.Context) error {
			return s.peerClient.Notify(ctx, peerID, payload)
		})
	})
}
