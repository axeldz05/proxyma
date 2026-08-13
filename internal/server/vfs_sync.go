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
	"sync"
)

func (s *Server) ExecuteSync() error {
	return s.ExecuteSyncContext(s.lifetimeCtx)
}

func (s *Server) ExecuteSyncContext(ctx context.Context) error {
	var (
		mu      sync.Mutex
		lastErr error
		anyOK   bool
	)
	s.forEachPeer(forEachPeerOpts{Context: ctx, Timeout: PeerRPCSync}, func(ctx context.Context, peerID string) error {
		s.ensureQUICSession(peerID)
		ctx = context.WithValue(ctx, p2p.BypassHolePunchKey{}, true)
		manifest, err := s.peerClient.FetchManifest(ctx, peerID)
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			mu.Lock()
			lastErr = err
			mu.Unlock()
			return err
		}
		_, manifestErr := s.Storage.ProcessRemoteManifestFromSource(manifest, peerID)
		if manifestErr != nil {
			s.Config.Logger.Warn("Sync skipped: manifest reconciliation failed", "peer", peerID, "error", manifestErr)
			mu.Lock()
			lastErr = manifestErr
			mu.Unlock()
			return manifestErr
		}

		// Push local entries the peer lacks (or has older). Critical after partition
		// heal: the isolated node can reach the sponsor, but the sponsor often cannot
		// dial the reconnected peer (DNS lag / stale IP) and has no relay of its own.
		snapshot, snapErr := s.Storage.GetVFSSnapshot()
		if snapErr != nil {
			s.Config.Logger.Error("Sync aborted: cannot load local VFS snapshot", "error", snapErr)
			mu.Lock()
			lastErr = snapErr
			mu.Unlock()
			return snapErr
		}
		for name, local := range snapshot {
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

		mu.Lock()
		anyOK = true
		mu.Unlock()
		return nil
	})
	if anyOK {
		return nil
	}
	return lastErr
}

func (s *Server) downloadWorker() {
	defer s.downloadWG.Done()
	for {
		if s.lifetimeCtx.Err() != nil {
			return
		}
		select {
		case <-s.lifetimeCtx.Done():
			return
		case job, ok := <-s.downloadQueue:
			if !ok {
				return
			}
			if s.lifetimeCtx.Err() != nil {
				return
			}
			if job.File.Deleted {
				if err := s.Storage.ProcessRemoteDeletion(job.File); err != nil {
					s.Config.Logger.Error(
						"Failed to process queued remote deletion",
						"file", job.File.Name,
						"source", job.Source,
						"error", err,
					)
				}
				continue
			}
			s.ensureQUICSession(job.Source)

			err := s.downloadJobFromPeer(job, job.Source)
			integrityFailure := errors.Is(err, storage.ErrBlobIntegrity)
			// Manifest source may only hold metadata (e.g. relay sponsor). Fall back
			// to any reachable peer that still has the physical blob — skip source so
			// callPeer does not re-mark it offline via a synthetic error.
			if err != nil && !errors.Is(err, storage.ErrBlobDiscarded) && s.lifetimeCtx.Err() == nil {
				for peerID := range s.GetPeersCopy() {
					if s.lifetimeCtx.Err() != nil {
						return
					}
					if peerID == s.Config.ID || peerID == job.Source {
						continue
					}
					fbErr := s.downloadJobFromPeer(job, peerID)
					if fbErr == nil {
						err = nil
						integrityFailure = false
						break
					}
					if errors.Is(fbErr, storage.ErrBlobIntegrity) {
						integrityFailure = true
					}
					if errors.Is(fbErr, storage.ErrBlobDiscarded) {
						err = fbErr
						break
					}
				}
			}
			if s.lifetimeCtx.Err() != nil {
				return
			}
			if errors.Is(err, storage.ErrBlobDiscarded) {
				s.Config.Logger.Debug("Blob download discarded due to obsolescence or deletion", "file", job.File.Name, "hash", job.File.Hash)
			} else if integrityFailure {
				if quarantineErr := s.Storage.QuarantineCorruptDownload(job.File); quarantineErr != nil {
					s.Config.Logger.Error("Failed to quarantine corrupt download intent", "file", job.File.Name, "error", quarantineErr)
				}
				s.Config.Logger.Error("Rejected corrupt blob source", "hash", job.File.Hash, "peer", job.Source, "error", err)
			} else if err != nil {
				s.Storage.ReleaseDownloadAttempt(job.File)
				s.Config.Logger.Error("Failed to download blob from peer", "hash", job.File.Hash, "peer", job.Source, "error", err)
			} else {
				s.Config.Logger.Info("Successfully downloaded and stored blob", "file", job.File.Name, "hash", job.File.Hash)
			}
		}
	}
}

func (s *Server) downloadJobFromPeer(job DownloadJob, peerID string) error {
	ctx, cancel := context.WithTimeout(s.lifetimeCtx, PeerRPCBlobLong)
	defer cancel()
	return s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
		return s.fetchBlobFromPeer(ctx, peerID, job.File)
	})
}

// fetchBlobFromPeer downloads a blob from peer and stores it locally (L2).
func (s *Server) fetchBlobFromPeer(ctx context.Context, peerID string, entry protocol.IndexEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := s.peerClient.DownloadBlob(ctx, peerID, entry.Hash)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Name != "" {
		return s.Storage.StoreRemoteBlob(entry, body)
	}
	return s.Storage.SaveVerifiedPhysicalBlob(entry.Hash, body)
}

func (s *Server) FetchFileOnDemand(name string) error {
	entry, ok, err := s.Storage.GetFileMetaE(name)
	if err != nil {
		return fmt.Errorf("failed to read VFS metadata for %q: %w", name, err)
	}
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

func (s *Server) LocalVFSList() ([]protocol.VFSFileStatus, error) {
	snapshot, err := s.Storage.GetVFSSnapshot()
	if err != nil {
		s.Config.Logger.Error("Failed to load VFS snapshot for list", "error", err)
		return nil, fmt.Errorf("failed to load VFS snapshot: %w", err)
	}
	upSpeed, downSpeed := s.GetCurrentBandwidth()

	var list []protocol.VFSFileStatus
	for name, entry := range snapshot {
		hasLocal, err := s.Storage.HasPhysicalBlob(entry.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect VFS blob %q: %w", entry.Hash, err)
		}
		isSubscribed, err := s.Storage.IsSubscribedE(name)
		if err != nil {
			return nil, fmt.Errorf("failed to read VFS subscription for %q: %w", name, err)
		}

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
	return list, nil
}

// announceAndSync announces to bootstrap (if set) then runs ExecuteSync.
func (s *Server) announceAndSync() error {
	if s.Config.BootstrapNode != "" {
		if err := s.AnnouncePresence(s.Config.BootstrapNode); err != nil {
			s.Config.Logger.Warn("Announce before sync failed", "bootstrap", s.Config.BootstrapNode, "error", err)
			syncErr := s.ExecuteSync()
			if syncErr != nil {
				return fmt.Errorf("announce failed: %v; sync failed: %w", err, syncErr)
			}
			return fmt.Errorf("announce failed: %w", err)
		}
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
	if err := s.Storage.SetSubscription(name, subscribe); err != nil {
		return err
	}
	if subscribe {
		s.goOwned(func() { _ = s.announceAndSync() })
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

func (s *Server) prepareVFSNotification(fileInfo protocol.IndexEntry) (func(bool) error, error) {
	payload := protocol.PeerNotification{
		File:   fileInfo,
		Source: s.Config.Address,
	}
	staged, err := s.prepareOutboxMutation(kindVFS, fileInfo.Name, payload)
	if err != nil {
		return nil, err
	}
	return staged.finish, nil
}
