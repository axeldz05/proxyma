package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage/physical"
	"time"

	"github.com/boltdb/bolt"
)

type StorageEngine struct {
	physical      storage.Storage
	vfs           *VFS
	subscriptions *bolt.DB
	downloadQueue chan DownloadJob
	logger        *slog.Logger
	peerClient    p2p.PeerClient
	notifyFunc    func(protocol.IndexEntry)
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func NewStorageEngine(logger *slog.Logger, path string, pc p2p.PeerClient, workers int, notify func(protocol.IndexEntry)) *StorageEngine {
	dbPath := filepath.Join(path, "metadata.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		logger.Error("Failed to open BoltDB", "path", dbPath, "error", err)
		os.Exit(1)
	}

	if err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte("subscriptions")); err != nil {
			logger.Error("Failed to create bucket for subscriptions", "error", err)
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte("vfs_index"))
		return err
	}); err != nil {
		logger.Error("Failed to create bucket for vfs_index", "error", err)
		os.Exit(1)
	}

	engine := &StorageEngine{
		physical:      *storage.NewStorage(path),
		vfs:           NewVFS(db),
		subscriptions: db,
		downloadQueue: make(chan DownloadJob, 1000),
		logger:        logger,
		peerClient:    pc,
		notifyFunc:    notify,
	}

	for range workers {
		go engine.downloadWorker()
	}

	return engine
}

func (se *StorageEngine) GetFileMeta(logicalName string) (protocol.IndexEntry, bool) {
	return se.vfs.Get(logicalName)
}

func (se *StorageEngine) HasPhysicalBlob(hash string) (bool, error) {
	return se.physical.BlobExists(hash)
}

func (se *StorageEngine) ReadPhysicalBlob(hash string, w io.Writer) error {
	return se.physical.ReadBlob(hash, w)
}

func (se *StorageEngine) SetSubscription(fileName string, isSubscribed bool) {
	err := se.subscriptions.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("subscriptions"))
		if isSubscribed {
			return b.Put([]byte(fileName), []byte("true"))
		}
		return b.Delete([]byte(fileName))
	})
	if err != nil {
		se.logger.Error("Failed to update subscription in DB", "file", fileName, "error", err)
	}
}

func (se *StorageEngine) isSubscribed(fileName string) bool {
	var subscribed bool
	_ = se.subscriptions.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("subscriptions"))
		if b != nil && b.Get([]byte(fileName)) != nil {
			subscribed = true
		}
		return nil
	})
	return subscribed
}

func (se *StorageEngine) GetVFSSnapshot() map[string]protocol.IndexEntry {
	return se.vfs.Snapshot()
}

func (se *StorageEngine) Upsert(entry protocol.IndexEntry) bool {
	return se.vfs.Upsert(entry)
}

// TODO: make it return the peers that couldn't connect to
func (se *StorageEngine) SyncStorage(peers map[string]string) error {
	for _, peerAddress := range peers {
		err := func(pAddr string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			remoteManifest, err := se.peerClient.FetchManifest(ctx, peerAddress)
			if err != nil {
				return err
			}
			for logicalName, remoteFileInfo := range remoteManifest {
				updated := se.vfs.Upsert(remoteFileInfo)
				if updated && !remoteFileInfo.Deleted {
					if se.isSubscribed(logicalName) {
						se.logger.Debug("DownloadJob added", "file", remoteFileInfo.Name, "source", peerAddress)
						se.downloadQueue <- DownloadJob{
							File:   remoteFileInfo,
							Source: peerAddress,
						}
					}
				}
			}
			return nil
		}(peerAddress)
		if err != nil {
			se.logger.Warn("Failed to synchronize with peer", "peerAddress", peerAddress, "error", err)
		}
	}
	return nil
}


func (se *StorageEngine) DeleteLocalFile(fileName string) error {
	entry, exists := se.vfs.Get(fileName)
	if !exists {
		return fmt.Errorf("file %se not found", fileName)
	}
	fileMeta := protocol.IndexEntry{
		Name:    entry.Name,
		Size:    entry.Size,
		Hash:    entry.Hash,
		Version: entry.Version + 1,
		Deleted: true,
	}
	if se.vfs.Upsert(fileMeta) {
		if err := se.physical.DeleteBlob(entry.Hash); err != nil {
			return fmt.Errorf("file %se could not be deleted", fileMeta.Name)
		}
		go se.notifyFunc(fileMeta)
	}
	return nil
}

func (se *StorageEngine) SaveLocalFile(fileName string, content io.Reader) error {
	hash, fileSize, err := se.physical.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("error saving the blob %se: %v", fileName, err.Error())
	}

	newVersion := 1
	if existingMeta, exists := se.vfs.Get(fileName); exists {
		newVersion = existingMeta.Version + 1
	}
	fileMeta := protocol.IndexEntry{
		Name:    fileName,
		Size:    fileSize,
		Hash:    hash,
		Version: newVersion,
	}
	se.vfs.Upsert(fileMeta)

	se.SetSubscription(fileName, true)

	go se.notifyFunc(fileMeta)

	return nil
}


func (se *StorageEngine) downloadFileFromPeer(fileInfo protocol.IndexEntry, sourceAddr string) {
	if fileInfo.Deleted {
		savedFileInfo, exists := se.vfs.Get(fileInfo.Name)
		if se.vfs.Upsert(fileInfo) {
			if exists {
				if err := se.physical.DeleteBlob(savedFileInfo.Hash); err != nil {
					se.logger.Error("Failed to delete blob", "file", fileInfo.Name, "error", err)
				}
			}
			se.logger.Info("File deleted", "file", fileInfo.Name)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := se.peerClient.DownloadBlob(ctx, sourceAddr, fileInfo.Hash)
	if err != nil {
		se.logger.Error("Failed to download blob", "file", fileInfo.Name, "error", err)
		return
	}
	defer func(){ _ = body.Close() }()

	savedHash, _, err := se.physical.SaveBlob(body)
	if err != nil {
		se.logger.Error("Failed to save blob", "file", fileInfo.Name, "error", err)
		return
	}
	if err := body.Close(); err != nil {
		se.logger.Error("Failed to close body", "error", err)
	}
	if savedHash != fileInfo.Hash {
		se.logger.Warn("SECURITY ALERT: Peer has sent corrupted or false hash", "expected", fileInfo.Hash, "got", savedHash)
		return
	}

	entry, exists := se.vfs.Get(fileInfo.Name)
	if exists && entry.Version == fileInfo.Version && !entry.Deleted {
		se.logger.Debug("Successfully downloaded and applied file", "file",  fileInfo.Name)
	} else {
		se.logger.Debug("Download discarded due to obsolescence or deletion while downloading", "file", fileInfo.Name, 
			"remote file version", fileInfo.Version, "current local version", entry.Version)
		if err := se.physical.DeleteBlob(fileInfo.Hash); err != nil {
			se.logger.Error("Failed to delete blob", "file", fileInfo.Name, "error", err)
		}
	}
}

func (se *StorageEngine) downloadWorker() {
	for job := range se.downloadQueue {
		se.downloadFileFromPeer(job.File, job.Source)
	}
}

func (c *StorageEngine) Close() {
	close(c.downloadQueue)
	if err := c.subscriptions.Close(); err != nil {
		c.logger.Error("Failed to close BoltDB safely", "error", err)
	}
}
