package storage

import (
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
	logger        *slog.Logger
	peerClient    p2p.PeerClient
	notifyFunc    func(protocol.IndexEntry)
	onDownloadNeeded func(file protocol.IndexEntry, rawSource string) error
}

func NewStorageEngine(logger *slog.Logger, path string, pc p2p.PeerClient, workers int, notify func(protocol.IndexEntry), downloadCallback func(protocol.IndexEntry, string) error) *StorageEngine {
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
		physical:      		*storage.NewStorage(path),
		vfs:           		NewVFS(db),
		subscriptions: 		db,
		logger:        		logger,
		peerClient:    		pc,
		notifyFunc:    		notify,
		onDownloadNeeded: 	downloadCallback,
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

func (se *StorageEngine) ProcessRemoteManifest(manifest map[string]protocol.IndexEntry) []protocol.IndexEntry {
	var missingFiles []protocol.IndexEntry
	for logicalName, remoteFileInfo := range manifest {
		updated := se.vfs.Upsert(remoteFileInfo)
		if !remoteFileInfo.Deleted && se.isSubscribed(logicalName) {
			hasBlob, err := se.HasPhysicalBlob(remoteFileInfo.Hash)
			if err != nil {
				se.logger.Error("Something happened while using HasPhysicalBlob", "error", err)
				continue
			}
			if updated || !hasBlob {
				se.logger.Debug("Missing file added", "file", remoteFileInfo.Name, "version", remoteFileInfo.Version, "hash", remoteFileInfo.Hash)
				missingFiles = append(missingFiles, remoteFileInfo)
			}
		}
	}
	return missingFiles
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

func (se *StorageEngine) ProcessRemoteDeletion(fileInfo protocol.IndexEntry) {
	savedFileInfo, exists := se.vfs.Get(fileInfo.Name)
	
	if se.vfs.Upsert(fileInfo) {
		if exists {
			if err := se.physical.DeleteBlob(savedFileInfo.Hash); err != nil {
				se.logger.Error("Failed to delete blob physically", "file", fileInfo.Name, "error", err)
			}
		}
		se.logger.Info("File remotely deleted", "file", fileInfo.Name)
	}
}

func (se *StorageEngine) StoreRemoteBlob(fileInfo protocol.IndexEntry, content io.Reader) error {
	savedHash, _, err := se.physical.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("failed to save blob physically: %w", err)
	}

	if savedHash != fileInfo.Hash {
		_ = se.physical.DeleteBlob(savedHash) 
		se.logger.Warn("SECURITY ALERT: Peer sent corrupted or false hash", "expected", fileInfo.Hash, "got", savedHash)
		return fmt.Errorf("hash mismatch")
	}

	entry, exists := se.vfs.Get(fileInfo.Name)
	if exists && entry.Version == fileInfo.Version && !entry.Deleted {
		se.logger.Debug("Successfully downloaded and applied file", "file", fileInfo.Name)
		return nil
	} 

	se.logger.Debug("Download discarded due to obsolescence or deletion while downloading", "file", fileInfo.Name)
	if err := se.physical.DeleteBlob(fileInfo.Hash); err != nil {
		se.logger.Error("Failed to delete obsolete blob", "file", fileInfo.Name, "error", err)
	}
	
	return nil
}
