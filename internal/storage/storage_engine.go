package storage

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	storage "proxyma/internal/storage/physical"
	"time"

	"github.com/boltdb/bolt"
)

type StorageEngine struct {
	physical         storage.Storage
	vfs              IndexStore
	subscriptions    *bolt.DB
	logger           *slog.Logger
	notifyFunc       func(protocol.IndexEntry)
	onDownloadNeeded func(file protocol.IndexEntry, rawSource string) error
}

func NewStorageEngine(logger *slog.Logger, path string, notify func(protocol.IndexEntry), downloadCallback func(protocol.IndexEntry, string) error) *StorageEngine {
	dbPath := filepath.Join(path, "metadata.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		logger.Error("Failed to open BoltDB", "path", dbPath, "error", err)
		os.Exit(1)
	}

	if err = db.Update(func(tx *bolt.Tx) error {
		for _, bName := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(bName)); err != nil {
				logger.Error("Failed to create bucket", "bucket", bName, "error", err)
				return err
			}
		}
		return nil
	}); err != nil {
		logger.Error("Failed to initialize database buckets", "error", err)
		os.Exit(1)
	}

	engine := &StorageEngine{
		physical:         *storage.NewStorage(path),
		vfs:              NewVFS(db),
		subscriptions:    db,
		logger:           logger,
		notifyFunc:       notify,
		onDownloadNeeded: downloadCallback,
	}

	return engine
}

func (se *StorageEngine) GetFileMeta(logicalName string) (protocol.IndexEntry, bool) {
	return se.vfs.Get(logicalName)
}

func (se *StorageEngine) HasPhysicalBlob(hash string) (bool, error) {
	return se.physical.BlobExists(hash)
}

func (se *StorageEngine) GetBlobPath(hash string) string {
	return se.physical.GetBlobPath(hash)
}

func (se *StorageEngine) SavePhysicalBlob(content io.Reader) (string, int64, error) {
	return se.physical.SaveBlob(content)
}

func (se *StorageEngine) ReadPhysicalBlob(hash string, w io.Writer) error {
	return se.physical.ReadBlob(hash, w)
}

func (se *StorageEngine) SetSubscription(fileName string, isSubscribed bool) {
	err := se.subscriptions.Update(func(tx *bolt.Tx) error {
		if isSubscribed {
			return boltPutFlag(tx, bucketSubscriptions, fileName)
		}
		return boltDelete(tx, bucketSubscriptions, fileName)
	})
	if err != nil {
		se.logger.Error("Failed to update subscription in DB", "file", fileName, "error", err)
	}
}

func (se *StorageEngine) IsSubscribed(fileName string) bool {
	var subscribed bool
	_ = se.subscriptions.View(func(tx *bolt.Tx) error {
		subscribed = boltHasKey(tx, bucketSubscriptions, fileName)
		return nil
	})
	return subscribed
}

// SetServiceSubscription records interest in a service name or prefix pattern (e.g. "ocr", "vision.*").
func (se *StorageEngine) SetServiceSubscription(pattern string, subscribed bool) {
	if pattern == "" {
		return
	}
	err := se.subscriptions.Update(func(tx *bolt.Tx) error {
		if subscribed {
			return boltPutFlag(tx, bucketServiceSubs, pattern)
		}
		return boltDelete(tx, bucketServiceSubs, pattern)
	})
	if err != nil {
		se.logger.Error("Failed to update service subscription", "pattern", pattern, "error", err)
	}
}

// HasServiceSubscriptions reports whether any service interest filters are active.
func (se *StorageEngine) HasServiceSubscriptions() bool {
	var n int
	_ = se.subscriptions.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketServiceSubs))
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n > 0
}

// IsServiceSubscribed returns true when name matches any stored pattern.
// If no service subscriptions exist, returns true (accept-all / join sync compat).
func (se *StorageEngine) IsServiceSubscribed(name string) bool {
	if !se.HasServiceSubscriptions() {
		return true
	}
	matched := false
	_ = se.subscriptions.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketServiceSubs))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if protocol.MatchServicePattern(string(k), name) {
				matched = true
				return nil
			}
		}
		return nil
	})
	return matched
}

func (se *StorageEngine) GetVFSSnapshot() map[string]protocol.IndexEntry {
	return se.vfs.Snapshot()
}

func (se *StorageEngine) Upsert(entry protocol.IndexEntry) bool {
	return se.upsertIndex(entry)
}

// upsertIndex writes metadata and logs an index failure instead of letting it
// look like "already up to date" (L2 SSOT for every internal upsert).
func (se *StorageEngine) upsertIndex(entry protocol.IndexEntry) bool {
	updated, err := se.vfs.Upsert(entry)
	if err != nil {
		se.logger.Error("Failed to persist VFS index entry", "file", entry.Name, "version", entry.Version, "error", err)
		return false
	}
	return updated
}

func (se *StorageEngine) ProcessRemoteManifest(manifest map[string]protocol.IndexEntry) []protocol.IndexEntry {
	var missingFiles []protocol.IndexEntry
	for logicalName, remoteFileInfo := range manifest {
		if remoteFileInfo.Deleted {
			se.ProcessRemoteDeletion(remoteFileInfo)
			continue
		}
		updated := se.upsertIndex(remoteFileInfo)
		if se.IsSubscribed(logicalName) {
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
		return fmt.Errorf("file %s not found", fileName)
	}
	fileMeta := protocol.IndexEntry{
		Name:    entry.Name,
		Size:    entry.Size,
		Hash:    entry.Hash,
		Version: entry.Version + 1,
		Deleted: true,
	}
	if se.upsertIndex(fileMeta) {
		if err := se.deleteBlobIfOrphan(entry.Hash, false); err != nil {
			return fmt.Errorf("file %s could not be deleted: %w", fileMeta.Name, err)
		}
		go se.notifyFunc(fileMeta)
	}
	return nil
}

func (se *StorageEngine) DeleteLocalCache(fileName string) error {
	entry, exists := se.vfs.Get(fileName)
	if !exists {
		return fmt.Errorf("file %s not found", fileName)
	}
	se.SetSubscription(fileName, false)
	_ = se.deleteBlobIfOrphan(entry.Hash, true)
	return nil
}

// UpsertAndSubscribe upserts VFS metadata with next version (if Version<=0), subscribes, optionally notifies (L2).
func (se *StorageEngine) UpsertAndSubscribe(entry protocol.IndexEntry, notify bool) protocol.IndexEntry {
	if entry.Version <= 0 {
		entry.Version = 1
		if existing, exists := se.vfs.Get(entry.Name); exists {
			entry.Version = existing.Version + 1
		}
	}
	se.upsertIndex(entry)
	se.SetSubscription(entry.Name, true)
	if notify {
		go se.notifyFunc(entry)
	}
	return entry
}

func (se *StorageEngine) SaveLocalFile(fileName string, content io.Reader) error {
	hash, fileSize, err := se.physical.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("error saving the blob %s: %v", fileName, err.Error())
	}
	se.UpsertAndSubscribe(protocol.IndexEntry{
		Name: fileName,
		Size: fileSize,
		Hash: hash,
	}, true)
	return nil
}

func (se *StorageEngine) ProcessRemoteDeletion(fileInfo protocol.IndexEntry) {
	savedFileInfo, exists := se.vfs.Get(fileInfo.Name)

	if se.upsertIndex(fileInfo) {
		if exists {
			if err := se.deleteBlobIfOrphan(savedFileInfo.Hash, false); err != nil {
				se.logger.Error("Failed to delete blob physically", "file", fileInfo.Name, "error", err)
			}
		}
		se.logger.Info("File remotely deleted", "file", fileInfo.Name)
	}
}

func (se *StorageEngine) StoreRemoteBlob(fileInfo protocol.IndexEntry, content io.Reader) error {
	if err := se.SaveVerifiedPhysicalBlob(fileInfo.Hash, content); err != nil {
		return err
	}

	entry, exists := se.vfs.Get(fileInfo.Name)
	if exists && entry.Version == fileInfo.Version && !entry.Deleted {
		se.logger.Debug("Successfully downloaded and applied file", "file", fileInfo.Name)
		return nil
	}

	se.logger.Debug("Download discarded due to obsolescence or deletion while downloading", "file", fileInfo.Name)
	if err := se.deleteBlobIfOrphan(fileInfo.Hash, false); err != nil {
		se.logger.Error("Failed to delete obsolete blob", "file", fileInfo.Name, "error", err)
	}

	return nil
}

// SaveVerifiedPhysicalBlob stores content and fails hard unless SHA-256 matches expectedHash (L2).
func (se *StorageEngine) SaveVerifiedPhysicalBlob(expectedHash string, content io.Reader) error {
	savedHash, _, err := se.physical.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("failed to save blob physically: %w", err)
	}
	if savedHash != expectedHash {
		_ = se.deleteBlobIfOrphan(savedHash, false)
		se.logger.Warn("SECURITY ALERT: Peer sent corrupted or false hash", "expected", expectedHash, "got", savedHash)
		return fmt.Errorf("hash mismatch")
	}
	return nil
}

// StageLocalFile opens a local path, saves it as a CAS blob, and upserts VFS metadata (L2).
// Logical VFS names are stage/<hash>/<basename> so same basenames from different paths do not collide.
func (se *StorageEngine) StageLocalFile(pathStr string) (hash string, size int64, err error) {
	fi, err := os.Stat(pathStr)
	if err != nil {
		return "", 0, err
	}
	if fi.IsDir() {
		return "", 0, fmt.Errorf("path is a directory: %s", pathStr)
	}
	f, err := os.Open(pathStr)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	hash, size, err = se.SavePhysicalBlob(f)
	if err != nil {
		return "", 0, err
	}
	name := "stage/" + hash + "/" + filepath.Base(pathStr)
	se.UpsertAndSubscribe(protocol.IndexEntry{
		Name: name,
		Hash: hash,
		Size: size,
	}, false)
	return hash, size, nil
}

// StageAndRewrite stages local file paths in m and rewrites them to vfs:// URIs (L2).
func (se *StorageEngine) StageAndRewrite(m map[string]any, annotateOutputs bool) {
	protocol.RewriteLocalFilePaths(m, se.StageLocalFile, annotateOutputs)
}

func (se *StorageEngine) CleanupTempFiles() {
	se.physical.CleanupTempFiles()
}

// deleteBlobIfOrphan removes the physical blob when no VFS refs remain.
// If subscribedOnly, only subscribed name refs count.
func (se *StorageEngine) deleteBlobIfOrphan(hash string, subscribedOnly bool) error {
	if se.countHashRefs(hash, subscribedOnly) > 0 {
		return nil
	}
	return se.physical.DeleteBlob(hash)
}

func (se *StorageEngine) countHashRefs(hash string, subscribedOnly bool) int {
	refCount := 0
	for name, entry := range se.vfs.Snapshot() {
		if entry.Deleted || entry.Hash != hash {
			continue
		}
		if subscribedOnly && !se.IsSubscribed(name) {
			continue
		}
		refCount++
	}
	return refCount
}

func (se *StorageEngine) boltPutKeyed(bucket, key string, v any) error {
	return se.subscriptions.Update(func(tx *bolt.Tx) error {
		return boltPutJSON(tx, bucket, key, v)
	})
}

func (se *StorageEngine) boltDeleteKeyed(bucket, key string) error {
	return se.subscriptions.Update(func(tx *bolt.Tx) error {
		return boltDelete(tx, bucket, key)
	})
}

func (se *StorageEngine) SavePeer(peerID string, record protocol.AddressRecord) error {
	return se.boltPutKeyed(bucketPeers, peerID, record)
}

func (se *StorageEngine) DeletePeer(peerID string) error {
	return se.boltDeleteKeyed(bucketPeers, peerID)
}

func (se *StorageEngine) LoadPeers() (map[string]protocol.AddressRecord, error) {
	return boltLoadMapJSON[protocol.AddressRecord](se.subscriptions, bucketPeers)
}

func (se *StorageEngine) Close() error {
	if se.subscriptions != nil {
		return se.subscriptions.Close()
	}
	return nil
}

func (se *StorageEngine) SavePipelineSchema(schema protocol.PipelineSchema) error {
	return se.boltPutKeyed(bucketPipelineSchemas, schema.ID, schema)
}

func (se *StorageEngine) DeletePipelineSchema(id string) error {
	return se.boltDeleteKeyed(bucketPipelineSchemas, id)
}

func (se *StorageEngine) LoadPipelineSchemas() (map[string]protocol.PipelineSchema, error) {
	return boltLoadMapJSON[protocol.PipelineSchema](se.subscriptions, bucketPipelineSchemas)
}

// PutOutboxRaw upserts a durable notify outbox entry (raw JSON bytes).
func (se *StorageEngine) PutOutboxRaw(id string, data []byte) error {
	return se.subscriptions.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return fmt.Errorf("notify_outbox bucket not found")
		}
		return b.Put([]byte(id), data)
	})
}

func (se *StorageEngine) DeleteOutboxEntry(id string) error {
	return se.boltDeleteKeyed(bucketNotifyOutbox, id)
}

func (se *StorageEngine) CountOutboxEntries() (int, error) {
	var n int
	err := se.subscriptions.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n, err
}

func (se *StorageEngine) ListOutboxRaw() (map[string][]byte, error) {
	out := make(map[string][]byte)
	err := se.subscriptions.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			cp := make([]byte, len(v))
			copy(cp, v)
			out[string(k)] = cp
			return nil
		})
	})
	return out, err
}
