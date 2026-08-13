package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	storage "proxyma/internal/storage/physical"
	"sort"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ErrBlobDiscarded is returned when a downloaded blob no longer matches the
// current VFS entry (obsolescence, deletion, or version mismatch).
var ErrBlobDiscarded = errors.New("blob discarded due to obsolescence or deletion")

// ErrBlobIntegrity marks content that can never satisfy the advertised hash.
// Callers must quarantine the current source intent instead of hot-looping it.
var ErrBlobIntegrity = errors.New("blob failed permanent integrity verification")

const (
	outboxGenerationPrefix  = "\x00outbox-generation\x00"
	outboxReservationPrefix = "\x00outbox-reservation\x00"
	downloadIntentPrefix    = "\x00download-intent\x00"
)

type downloadIntent struct {
	File   protocol.IndexEntry `json:"file"`
	Source string              `json:"source"`
}

type activeDownload struct {
	Intent downloadIntent
	Token  uint64
}

type StorageEngine struct {
	physical         *storage.Storage
	vfs              IndexStore
	subscriptions    *bbolt.DB
	logger           *slog.Logger
	notifyFunc       func(protocol.IndexEntry)
	mutationNotify   func(protocol.IndexEntry) (func(bool) error, error)
	onDownloadNeeded func(file protocol.IndexEntry, rawSource string) error

	mutationMu      sync.Mutex
	downloadMu      sync.Mutex
	nextDownloadID  uint64
	activeDownloads map[string]activeDownload
}

func NewStorageEngine(logger *slog.Logger, path string, notify func(protocol.IndexEntry), downloadCallback func(protocol.IndexEntry, string) error) (*StorageEngine, error) {
	dbPath := filepath.Join(path, "metadata.db")
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open metadata db %s: %w", dbPath, err)
	}

	if err = db.Update(func(tx *bbolt.Tx) error {
		for _, bName := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(bName)); err != nil {
				return fmt.Errorf("create bucket %s: %w", bName, err)
			}
		}
		return migrateOutboxNamespaces(tx)
	}); err != nil {
		// Release the file lock, or a retry in this process burns the Open timeout.
		_ = db.Close()
		return nil, fmt.Errorf("initialize metadata db %s: %w", dbPath, err)
	}

	engine := &StorageEngine{
		physical:         storage.NewStorage(path),
		vfs:              NewVFS(db),
		subscriptions:    db,
		logger:           logger,
		notifyFunc:       notify,
		onDownloadNeeded: downloadCallback,
		activeDownloads:  make(map[string]activeDownload),
	}
	if err := engine.SweepPendingBlobGC(); err != nil {
		logger.Warn("Failed to sweep pending blob GC at startup", "error", err)
	}
	return engine, nil
}

func (se *StorageEngine) GetFileMeta(logicalName string) (protocol.IndexEntry, bool) {
	entry, exists, err := se.GetFileMetaE(logicalName)
	if err != nil {
		se.logger.Error("Failed to read VFS metadata", "file", logicalName, "error", err)
		return protocol.IndexEntry{}, false
	}
	return entry, exists
}

// GetFileMetaE is the error-preserving metadata read API. GetFileMeta remains
// as a compatibility wrapper for callers that cannot yet accept an error.
func (se *StorageEngine) GetFileMetaE(logicalName string) (protocol.IndexEntry, bool, error) {
	return se.vfs.Get(logicalName)
}

// SetMutationNotificationHook installs a write-ahead notification hook. The
// returned completion callback receives whether metadata committed.
func (se *StorageEngine) SetMutationNotificationHook(
	hook func(protocol.IndexEntry) (func(bool) error, error),
) {
	se.mutationNotify = hook
}

func (se *StorageEngine) prepareMutationNotification(entry protocol.IndexEntry) (func(bool) error, error) {
	if se.mutationNotify != nil {
		return se.mutationNotify(entry)
	}
	if se.notifyFunc == nil {
		return func(bool) error { return nil }, nil
	}
	return func(committed bool) error {
		if committed {
			se.notifyFunc(entry)
		}
		return nil
	}, nil
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

func (se *StorageEngine) SetSubscription(fileName string, isSubscribed bool) error {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	return se.setSubscriptionLocked(fileName, isSubscribed)
}

func (se *StorageEngine) setSubscriptionLocked(fileName string, isSubscribed bool) error {
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		if isSubscribed {
			return boltPutFlag(tx, bucketSubscriptions, fileName)
		}
		return boltDelete(tx, bucketSubscriptions, fileName)
	})
	if err != nil {
		se.logger.Error("Failed to update subscription in DB", "file", fileName, "error", err)
		return err
	}
	return nil
}

func (se *StorageEngine) IsSubscribed(fileName string) bool {
	subscribed, err := se.IsSubscribedE(fileName)
	if err != nil {
		se.logger.Error("Failed to read subscription", "file", fileName, "error", err)
		return false
	}
	return subscribed
}

func (se *StorageEngine) IsSubscribedE(fileName string) (bool, error) {
	var subscribed bool
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		var err error
		subscribed, err = boltHasKey(tx, bucketSubscriptions, fileName)
		return err
	})
	return subscribed, err
}

// SetServiceSubscription records interest in a service name or prefix pattern (e.g. "ocr", "vision.*").
func (se *StorageEngine) SetServiceSubscription(pattern string, subscribed bool) error {
	if pattern == "" {
		return fmt.Errorf("empty service subscription pattern")
	}
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		if subscribed {
			return boltPutFlag(tx, bucketServiceSubs, pattern)
		}
		return boltDelete(tx, bucketServiceSubs, pattern)
	})
	if err != nil {
		se.logger.Error("Failed to update service subscription", "pattern", pattern, "error", err)
		return err
	}
	return nil
}

// HasServiceSubscriptions reports whether any service interest filters are active.
func (se *StorageEngine) HasServiceSubscriptions() bool {
	hasSubscriptions, err := se.HasServiceSubscriptionsE()
	if err != nil {
		se.logger.Error("Failed to read service subscriptions", "error", err)
		return false
	}
	return hasSubscriptions
}

func (se *StorageEngine) HasServiceSubscriptionsE() (bool, error) {
	var n int
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketServiceSubs))
		if b == nil {
			return fmt.Errorf("%s bucket not found", bucketServiceSubs)
		}
		n = b.Stats().KeyN
		return nil
	})
	return n > 0, err
}

// IsServiceSubscribed returns true when name matches any stored pattern.
// If no service subscriptions exist, returns true (accept-all / join sync compat).
func (se *StorageEngine) IsServiceSubscribed(name string) bool {
	subscribed, err := se.IsServiceSubscribedE(name)
	if err != nil {
		se.logger.Error("Failed to match service subscription", "service", name, "error", err)
		return false
	}
	return subscribed
}

func (se *StorageEngine) IsServiceSubscribedE(name string) (bool, error) {
	matched := false
	hasSubscriptions := false
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketServiceSubs))
		if b == nil {
			return fmt.Errorf("%s bucket not found", bucketServiceSubs)
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			hasSubscriptions = true
			if protocol.MatchServicePattern(string(k), name) {
				matched = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if !hasSubscriptions {
		return true, nil
	}
	return matched, nil
}

func (se *StorageEngine) GetVFSSnapshot() (map[string]protocol.IndexEntry, error) {
	return se.vfs.Snapshot()
}

func (se *StorageEngine) Upsert(entry protocol.IndexEntry) (bool, error) {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	return se.upsertIndexLocked(entry)
}

// upsertIndex writes metadata and surfaces DB failures separately from
// "already up to date" (updated=false, err=nil).
func (se *StorageEngine) upsertIndex(entry protocol.IndexEntry) (bool, error) {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	return se.upsertIndexLocked(entry)
}

func (se *StorageEngine) upsertIndexLocked(entry protocol.IndexEntry) (bool, error) {
	previous, existed, err := se.vfs.Get(entry.Name)
	if err != nil {
		se.logger.Error("Failed to read current VFS index entry", "file", entry.Name, "error", err)
		return false, err
	}
	updated, err := se.vfs.Upsert(entry)
	if err != nil {
		se.logger.Error("Failed to persist VFS index entry", "file", entry.Name, "version", entry.Version, "error", err)
		return false, err
	}
	if updated {
		if _, native := se.vfs.(*VFS); !native &&
			existed &&
			previous.Hash != "" &&
			(entry.Deleted || previous.Hash != entry.Hash) {
			if err := se.queueBlobGCLocked(previous.Hash); err != nil {
				return true, fmt.Errorf("VFS metadata for %s committed but GC intent failed: %w", entry.Name, err)
			}
		}
		if err := se.sweepPendingBlobGCLocked(); err != nil {
			return true, fmt.Errorf(
				"VFS metadata for %s committed but pending blob GC failed: %w",
				entry.Name,
				err,
			)
		}
	}
	return updated, nil
}

func (se *StorageEngine) ProcessRemoteManifest(manifest map[string]protocol.IndexEntry) []protocol.IndexEntry {
	missing, err := se.ProcessRemoteManifestE(manifest)
	if err != nil {
		se.logger.Error("Failed to process remote manifest", "error", err)
	}
	return missing
}

// ProcessRemoteManifestE processes every entry in deterministic name order,
// preserving successful metadata decisions while joining per-entry failures.
// ProcessRemoteManifest remains as a compatibility wrapper for its server caller.
func (se *StorageEngine) ProcessRemoteManifestE(manifest map[string]protocol.IndexEntry) ([]protocol.IndexEntry, error) {
	var missingFiles []protocol.IndexEntry
	var manifestErr error
	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		remoteFileInfo := manifest[name]
		if remoteFileInfo.Deleted {
			if err := se.ProcessRemoteDeletion(remoteFileInfo); err != nil {
				manifestErr = errors.Join(
					manifestErr,
					fmt.Errorf("process manifest tombstone %q: %w", remoteFileInfo.Name, err),
				)
			}
			continue
		}
		updated, err := se.upsertIndex(remoteFileInfo)
		if err != nil {
			manifestErr = errors.Join(
				manifestErr,
				fmt.Errorf("persist manifest entry %q: %w", remoteFileInfo.Name, err),
			)
			continue
		}
		current, exists, err := se.GetFileMetaE(remoteFileInfo.Name)
		if err != nil {
			manifestErr = errors.Join(
				manifestErr,
				fmt.Errorf("read manifest entry %q: %w", remoteFileInfo.Name, err),
			)
			continue
		}
		if !exists || current != remoteFileInfo {
			continue
		}
		subscribed, err := se.IsSubscribedE(current.Name)
		if err != nil {
			manifestErr = errors.Join(
				manifestErr,
				fmt.Errorf("read manifest subscription %q: %w", current.Name, err),
			)
			continue
		}
		if !subscribed {
			continue
		}
		hasBlob, err := se.HasPhysicalBlob(current.Hash)
		if err != nil {
			manifestErr = errors.Join(
				manifestErr,
				fmt.Errorf("verify manifest blob %q: %w", current.Name, err),
			)
			continue
		}
		if updated || !hasBlob {
			se.logger.Debug("Missing file added", "file", current.Name, "version", current.Version, "hash", current.Hash)
			missingFiles = append(missingFiles, current)
		}
	}
	return missingFiles, manifestErr
}

// ProcessRemoteManifestFromSource persists durable download intents for every
// subscribed missing blob before invoking the enqueue callback.
func (se *StorageEngine) ProcessRemoteManifestFromSource(
	manifest map[string]protocol.IndexEntry,
	source string,
) ([]protocol.IndexEntry, error) {
	missing, manifestErr := se.ProcessRemoteManifestE(manifest)
	for _, file := range missing {
		if err := se.requestRemoteDownload(file, source); err != nil {
			manifestErr = errors.Join(
				manifestErr,
				fmt.Errorf("stage manifest download %q: %w", file.Name, err),
			)
		}
	}
	return missing, manifestErr
}

func (se *StorageEngine) DeleteLocalFile(fileName string) (err error) {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	entry, exists, err := se.vfs.Get(fileName)
	if err != nil {
		return err
	}
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
	finishNotification, err := se.prepareMutationNotification(fileMeta)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		err = errors.Join(err, finishNotification(committed))
	}()
	updated, err := se.vfs.Upsert(fileMeta)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("failed to persist deletion tombstone for %s", fileName)
	}
	committed = true
	if err := se.sweepPendingBlobGCLocked(); err != nil {
		// Tombstone + notify already committed; surface GC failure without hiding the delete.
		return fmt.Errorf("file %s tombstoned but blob GC failed: %w", fileMeta.Name, err)
	}
	return nil
}

func (se *StorageEngine) DeleteLocalCache(fileName string) error {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	entry, exists, err := se.vfs.Get(fileName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("file %s not found", fileName)
	}
	if err := se.setSubscriptionLocked(fileName, false); err != nil {
		return err
	}
	return se.deleteBlobIfOrphan(entry.Hash, true)
}

// UpsertAndSubscribe upserts VFS metadata with next version (if Version<=0), subscribes, optionally notifies (L2).
func (se *StorageEngine) UpsertAndSubscribe(entry protocol.IndexEntry, notify bool) (protocol.IndexEntry, error) {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	result, err := se.upsertAndSubscribeLocked(entry, notify)
	return result.Entry, err
}

func (se *StorageEngine) upsertAndSubscribeLocked(
	entry protocol.IndexEntry,
	shouldNotify bool,
) (result indexSubscriptionMutation, err error) {
	var finishNotification func(bool) error
	if shouldNotify {
		current, exists, readErr := se.vfs.Get(entry.Name)
		if readErr != nil {
			return result, readErr
		}
		if entry.Version <= 0 {
			entry.Version = 1
			if exists {
				entry.Version = current.Version + 1
			}
		} else if exists && compareIndexEntries(entry, current) <= 0 {
			return indexSubscriptionMutation{
				Entry:       current,
				Previous:    current,
				HadPrevious: true,
			}, nil
		}
		finishNotification, err = se.prepareMutationNotification(entry)
		if err != nil {
			return result, err
		}
		defer func() {
			err = errors.Join(err, finishNotification(result.Applied))
		}()
	}
	if vfs, ok := se.vfs.(*VFS); ok && vfs.index == se.subscriptions {
		result, err = vfs.upsertAndSubscribe(entry)
	} else {
		result.Entry = entry
		result.Previous, result.HadPrevious, err = se.vfs.Get(entry.Name)
		if err == nil && entry.Version <= 0 {
			result.Entry, err = se.vfs.UpsertAutoVersion(entry)
			result.Applied = err == nil
		} else if err == nil {
			result.Applied, err = se.vfs.Upsert(entry)
			if !result.Applied {
				current, exists, readErr := se.vfs.Get(entry.Name)
				if readErr != nil {
					err = readErr
				} else if exists {
					result.Entry = current
				}
			}
		}
		if err == nil && result.Applied {
			err = se.setSubscriptionLocked(result.Entry.Name, true)
		}
	}
	if err != nil {
		return result, err
	}
	if !result.Applied {
		return result, nil
	}

	if _, native := se.vfs.(*VFS); !native &&
		result.HadPrevious &&
		result.Previous.Hash != "" &&
		(result.Entry.Deleted || result.Previous.Hash != result.Entry.Hash) {
		if err := se.queueBlobGCLocked(result.Previous.Hash); err != nil {
			return result, fmt.Errorf(
				"VFS metadata for %s committed but GC intent failed: %w",
				result.Entry.Name,
				err,
			)
		}
	}
	if err := se.sweepPendingBlobGCLocked(); err != nil {
		return result, fmt.Errorf(
			"VFS metadata for %s committed but pending blob GC failed: %w",
			result.Entry.Name,
			err,
		)
	}
	return result, nil
}

func (se *StorageEngine) SaveLocalFile(fileName string, content io.Reader) error {
	staged, err := se.physical.StageBlob(content)
	if err != nil {
		return fmt.Errorf("error staging the blob %s: %w", fileName, err)
	}
	defer func() { _ = staged.Discard() }()
	if err := staged.Prepare(); err != nil {
		return fmt.Errorf("error preparing the blob %s: %w", fileName, err)
	}
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	created, err := staged.Commit()
	if err != nil {
		return fmt.Errorf("error committing the blob %s: %w", fileName, err)
	}
	result, err := se.upsertAndSubscribeLocked(protocol.IndexEntry{
		Name: fileName,
		Size: staged.Size(),
		Hash: staged.Hash(),
	}, true)
	if err != nil && !result.Applied {
		if cleanupErr := se.compensatePhysicalBlob(staged.Hash(), created); cleanupErr != nil {
			return errors.Join(err, fmt.Errorf("compensate blob %s: %w", staged.Hash(), cleanupErr))
		}
	}
	return err
}

func (se *StorageEngine) ProcessRemoteDeletion(fileInfo protocol.IndexEntry) error {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	updated, err := se.upsertIndexLocked(fileInfo)
	if err != nil {
		return err
	}
	current, exists, err := se.vfs.Get(fileInfo.Name)
	if err != nil {
		return err
	}
	if !exists || current != fileInfo || !current.Deleted {
		return nil
	}
	if err := se.completeDownloadIntent(fileInfo); err != nil {
		return err
	}
	if updated {
		se.logger.Info("File remotely deleted", "file", fileInfo.Name)
	}
	return nil
}

func (se *StorageEngine) StoreRemoteBlob(fileInfo protocol.IndexEntry, content io.Reader) error {
	staged, err := se.physical.StageBlob(content)
	if err != nil {
		se.releaseDownloadIntent(fileInfo)
		return fmt.Errorf("failed to stage blob physically: %w", err)
	}
	defer func() { _ = staged.Discard() }()
	if staged.Hash() != fileInfo.Hash {
		se.releaseDownloadIntent(fileInfo)
		se.logger.Warn("SECURITY ALERT: Peer sent corrupted or false hash", "expected", fileInfo.Hash, "got", staged.Hash())
		return errors.Join(ErrBlobIntegrity, fmt.Errorf("hash mismatch"))
	}
	if err := staged.Prepare(); err != nil {
		se.releaseDownloadIntent(fileInfo)
		return fmt.Errorf("failed to prepare blob physically: %w", err)
	}
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	created, err := staged.Commit()
	if err != nil {
		se.releaseDownloadIntent(fileInfo)
		return fmt.Errorf("failed to commit blob physically: %w", err)
	}

	entry, exists, err := se.vfs.Get(fileInfo.Name)
	if err != nil {
		if cleanupErr := se.compensatePhysicalBlob(fileInfo.Hash, created); cleanupErr != nil {
			return errors.Join(err, fmt.Errorf("compensate remote blob: %w", cleanupErr))
		}
		return err
	}
	if exists &&
		entry.Name == fileInfo.Name &&
		entry.Version == fileInfo.Version &&
		entry.Hash == fileInfo.Hash &&
		entry.Deleted == fileInfo.Deleted &&
		!entry.Deleted {
		if err := se.completeDownloadIntent(fileInfo); err != nil {
			return err
		}
		se.logger.Debug("Successfully downloaded and applied file", "file", fileInfo.Name)
		return nil
	}

	se.logger.Debug("Download discarded due to obsolescence or deletion while downloading", "file", fileInfo.Name)
	gcErr := se.deleteBlobIfOrphan(fileInfo.Hash, false)
	if gcErr != nil {
		se.logger.Error("Failed to delete obsolete blob", "file", fileInfo.Name, "error", gcErr)
	}
	completeErr := se.completeDownloadIntent(fileInfo)

	return errors.Join(ErrBlobDiscarded, gcErr, completeErr)
}

// SaveVerifiedPhysicalBlob stores content and fails hard unless SHA-256 matches expectedHash (L2).
func (se *StorageEngine) SaveVerifiedPhysicalBlob(expectedHash string, content io.Reader) error {
	staged, err := se.physical.StageBlob(content)
	if err != nil {
		return fmt.Errorf("failed to stage blob physically: %w", err)
	}
	defer func() { _ = staged.Discard() }()
	if staged.Hash() != expectedHash {
		se.logger.Warn("SECURITY ALERT: Peer sent corrupted or false hash", "expected", expectedHash, "got", staged.Hash())
		return errors.Join(ErrBlobIntegrity, fmt.Errorf("hash mismatch"))
	}
	if err := staged.Prepare(); err != nil {
		return fmt.Errorf("failed to prepare blob physically: %w", err)
	}
	_, err = staged.Commit()
	return err
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
	staged, err := se.physical.StageBlob(f)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = staged.Discard() }()
	if err := staged.Prepare(); err != nil {
		return "", 0, err
	}
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	created, err := staged.Commit()
	if err != nil {
		return "", 0, err
	}
	hash, size = staged.Hash(), staged.Size()
	name := "stage/" + hash + "/" + filepath.Base(pathStr)
	result, err := se.upsertAndSubscribeLocked(protocol.IndexEntry{
		Name: name,
		Hash: hash,
		Size: size,
	}, false)
	if err != nil {
		if !result.Applied {
			if cleanupErr := se.compensatePhysicalBlob(hash, created); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("compensate staged blob: %w", cleanupErr))
			}
		}
		return "", 0, err
	}
	return hash, size, nil
}

func (se *StorageEngine) compensatePhysicalBlob(hash string, created bool) error {
	if !created {
		return nil
	}
	return se.physical.DeleteBlob(hash)
}

// StageAndRewrite stages local file paths in m and rewrites them to vfs:// URIs (L2).
func (se *StorageEngine) StageAndRewrite(m map[string]any, annotateOutputs bool) error {
	return protocol.RewriteLocalFilePaths(m, se.StageLocalFile, annotateOutputs)
}

func (se *StorageEngine) CleanupTempFiles() {
	se.physical.CleanupTempFiles()
}

// deleteBlobIfOrphan removes the physical blob when no VFS refs remain.
// If subscribedOnly, only subscribed name refs count.
// On Snapshot failure it refuses to delete (never treat a corrupt index as empty).
func (se *StorageEngine) deleteBlobIfOrphan(hash string, subscribedOnly bool) error {
	refs, err := se.countHashRefs(hash, subscribedOnly)
	if err != nil {
		return fmt.Errorf("orphan check aborted: %w", err)
	}
	if refs > 0 {
		return nil
	}
	return se.physical.DeleteBlob(hash)
}

func (se *StorageEngine) countHashRefs(hash string, subscribedOnly bool) (int, error) {
	if vfs, ok := se.vfs.(*VFS); ok && vfs.index == se.subscriptions {
		refCount := 0
		err := se.subscriptions.View(func(tx *bbolt.Tx) error {
			index := tx.Bucket([]byte(vfsIndexBucket))
			if index == nil {
				return fmt.Errorf("%s bucket not found", vfsIndexBucket)
			}
			subscriptions := tx.Bucket([]byte(bucketSubscriptions))
			if subscribedOnly && subscriptions == nil {
				return fmt.Errorf("%s bucket not found", bucketSubscriptions)
			}
			return index.ForEach(func(k, raw []byte) error {
				var entry protocol.IndexEntry
				if err := json.Unmarshal(raw, &entry); err != nil {
					return fmt.Errorf("corrupt JSON in %s/%s: %w", vfsIndexBucket, string(k), err)
				}
				if entry.Deleted || entry.Hash != hash {
					return nil
				}
				if subscribedOnly && subscriptions.Get(k) == nil {
					return nil
				}
				refCount++
				return nil
			})
		})
		return refCount, err
	}

	snapshot, err := se.vfs.Snapshot()
	if err != nil {
		return 0, err
	}
	refCount := 0
	for name, entry := range snapshot {
		if entry.Deleted || entry.Hash != hash {
			continue
		}
		if subscribedOnly {
			subscribed, err := se.IsSubscribedE(name)
			if err != nil {
				return 0, err
			}
			if !subscribed {
				continue
			}
		}
		refCount++
	}
	return refCount, nil
}

func (se *StorageEngine) queueBlobGCLocked(hash string) error {
	if hash == "" {
		return nil
	}
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		return boltPutFlag(tx, bucketPendingBlobGC, hash)
	})
}

// SweepPendingBlobGC retries durable cleanup intents. Metadata mutators queue a
// superseded hash in the same bbolt transaction as the winning VFS entry, so a
// crash or physical deletion failure cannot permanently forget the orphan.
func (se *StorageEngine) SweepPendingBlobGC() error {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	return se.sweepPendingBlobGCLocked()
}

func (se *StorageEngine) sweepPendingBlobGCLocked() error {
	pending := make(map[string]bool)
	referenced := make(map[string]bool)
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		gcBucket := tx.Bucket([]byte(bucketPendingBlobGC))
		if gcBucket == nil {
			return fmt.Errorf("%s bucket not found", bucketPendingBlobGC)
		}
		if err := gcBucket.ForEach(func(k, _ []byte) error {
			pending[string(k)] = true
			return nil
		}); err != nil {
			return err
		}
		index := tx.Bucket([]byte(vfsIndexBucket))
		if index == nil {
			return fmt.Errorf("%s bucket not found", vfsIndexBucket)
		}
		return index.ForEach(func(k, raw []byte) error {
			var entry protocol.IndexEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("corrupt JSON in %s/%s: %w", vfsIndexBucket, string(k), err)
			}
			if !entry.Deleted && pending[entry.Hash] {
				referenced[entry.Hash] = true
			}
			return nil
		})
	})
	if err != nil || len(pending) == 0 {
		return err
	}

	completed := make(map[string]bool)
	var sweepErr error
	for hash := range pending {
		if referenced[hash] {
			completed[hash] = true
			continue
		}
		if err := se.physical.DeleteBlob(hash); err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("delete pending blob %s: %w", hash, err))
			continue
		}
		completed[hash] = true
	}
	if len(completed) != 0 {
		if err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
			bucket := tx.Bucket([]byte(bucketPendingBlobGC))
			if bucket == nil {
				return fmt.Errorf("%s bucket not found", bucketPendingBlobGC)
			}
			for hash := range completed {
				if err := bucket.Delete([]byte(hash)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	return sweepErr
}

func (se *StorageEngine) pendingBlobGCCount() (int, error) {
	count := 0
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketPendingBlobGC))
		if bucket == nil {
			return fmt.Errorf("%s bucket not found", bucketPendingBlobGC)
		}
		count = bucket.Stats().KeyN
		return nil
	})
	return count, err
}

func (se *StorageEngine) boltPutKeyed(bucket, key string, v any) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		return boltPutJSON(tx, bucket, key, v)
	})
}

func (se *StorageEngine) boltDeleteKeyed(bucket, key string) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
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

func (se *StorageEngine) SaveInvite(secret string, expiration time.Time) error {
	return se.boltPutKeyed(bucketPendingInvites, secret, expiration.UTC())
}

func (se *StorageEngine) DeleteInvite(secret string) error {
	return se.boltDeleteKeyed(bucketPendingInvites, secret)
}

func (se *StorageEngine) LoadInvites() (map[string]time.Time, error) {
	return boltLoadMapJSON[time.Time](se.subscriptions, bucketPendingInvites)
}

func (se *StorageEngine) Close() error {
	if se.subscriptions != nil {
		return se.subscriptions.Close()
	}
	return nil
}

func (se *StorageEngine) SavePipelineSchema(schema protocol.PipelineSchema) error {
	schema = protocol.NormalizePipelineSchemaVersion(schema)
	return se.boltPutKeyed(bucketPipelineSchemas, schema.ID, schema)
}

func (se *StorageEngine) DeletePipelineSchema(id string) error {
	return se.boltDeleteKeyed(bucketPipelineSchemas, id)
}

func (se *StorageEngine) LoadPipelineSchemas() (map[string]protocol.PipelineSchema, error) {
	schemas, err := boltLoadMapJSON[protocol.PipelineSchema](se.subscriptions, bucketPipelineSchemas)
	if err != nil {
		return nil, err
	}
	var normalized map[string]protocol.PipelineSchema
	for id, schema := range schemas {
		next := protocol.NormalizePipelineSchemaVersion(schema)
		if next.Version == schema.Version {
			continue
		}
		if normalized == nil {
			normalized = make(map[string]protocol.PipelineSchema)
		}
		normalized[id] = next
		schemas[id] = next
	}
	if len(normalized) != 0 {
		err = se.subscriptions.Update(func(tx *bbolt.Tx) error {
			for id, schema := range normalized {
				if err := boltPutJSON(tx, bucketPipelineSchemas, id, schema); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return schemas, err
}

func outboxGlobalGenerationKey() []byte {
	return []byte("global")
}

func downloadIntentKey(name string) string {
	return downloadIntentPrefix + base64.RawURLEncoding.EncodeToString([]byte(name))
}

func downloadIntentStorageKey(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

func isRecognizedLegacyOutboxMetadata(key, value []byte) bool {
	if (bytes.HasPrefix(key, []byte(outboxGenerationPrefix)) ||
		bytes.HasPrefix(key, []byte(outboxReservationPrefix))) && len(value) == 8 {
		return true
	}
	if !bytes.HasPrefix(key, []byte(downloadIntentPrefix)) {
		return false
	}
	var intent downloadIntent
	return json.Unmarshal(value, &intent) == nil && intent.File.Name != ""
}

// migrateOutboxNamespaces moves only structurally recognized metadata. Legacy
// notification rows stay isolated for payload-aware server reconciliation.
func migrateOutboxNamespaces(tx *bbolt.Tx) error {
	legacy := tx.Bucket([]byte(bucketNotifyOutbox))
	generations := tx.Bucket([]byte(bucketNotifyOutboxV2Generations))
	reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
	intents := tx.Bucket([]byte(bucketDownloadIntents))
	if legacy == nil || generations == nil || reservations == nil || intents == nil {
		return fmt.Errorf("outbox namespace bucket not found")
	}

	var generation uint64
	if raw := generations.Get(outboxGlobalGenerationKey()); len(raw) != 0 {
		if len(raw) != 8 {
			return fmt.Errorf("invalid global outbox generation")
		}
		generation = binary.BigEndian.Uint64(raw)
	}
	var removeLegacy [][]byte
	err := legacy.ForEach(func(key, value []byte) error {
		switch {
		case bytes.HasPrefix(key, []byte(outboxGenerationPrefix)) && len(value) == 8:
			if candidate := binary.BigEndian.Uint64(value); candidate > generation {
				generation = candidate
			}
			removeLegacy = append(removeLegacy, bytes.Clone(key))
		case bytes.HasPrefix(key, []byte(outboxReservationPrefix)) && len(value) == 8:
			removeLegacy = append(removeLegacy, bytes.Clone(key))
		case bytes.HasPrefix(key, []byte(downloadIntentPrefix)):
			var intent downloadIntent
			if json.Unmarshal(value, &intent) != nil || intent.File.Name == "" {
				return nil
			}
			if err := intents.Put([]byte(downloadIntentStorageKey(intent.File.Name)), value); err != nil {
				return err
			}
			removeLegacy = append(removeLegacy, bytes.Clone(key))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if generation != 0 {
		raw := make([]byte, 8)
		binary.BigEndian.PutUint64(raw, generation)
		if err := generations.Put(outboxGlobalGenerationKey(), raw); err != nil {
			return err
		}
	}
	for _, key := range removeLegacy {
		if err := legacy.Delete(key); err != nil {
			return err
		}
	}
	// Reservations represent in-process commits. None can survive a restart,
	// so clearing them bounds metadata without weakening ordering.
	var staleReservations [][]byte
	if err := reservations.ForEach(func(key, _ []byte) error {
		staleReservations = append(staleReservations, bytes.Clone(key))
		return nil
	}); err != nil {
		return err
	}
	for _, key := range staleReservations {
		if err := reservations.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// ReserveOutboxGeneration allocates a globally monotonic durable token and a
// transient per-key reservation. Successful Put reclaims the reservation, so
// generation metadata remains bounded without permitting token reuse.
func (se *StorageEngine) ReserveOutboxGeneration(id string) (uint64, error) {
	var generation uint64
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		var err error
		generation, err = reserveOutboxGenerationTx(tx, id)
		return err
	})
	return generation, err
}

func reserveOutboxGenerationTx(tx *bbolt.Tx, id string) (uint64, error) {
	generations := tx.Bucket([]byte(bucketNotifyOutboxV2Generations))
	reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
	if generations == nil || reservations == nil {
		return 0, fmt.Errorf("outbox v2 metadata bucket not found")
	}
	globalKey := outboxGlobalGenerationKey()
	raw := generations.Get(globalKey)
	if len(raw) != 0 && len(raw) != 8 {
		return 0, fmt.Errorf("invalid global outbox generation")
	}
	var generation uint64
	if len(raw) == 8 {
		generation = binary.BigEndian.Uint64(raw)
	}
	if generation == ^uint64(0) {
		return 0, fmt.Errorf("outbox generation exhausted")
	}
	generation++
	next := make([]byte, 8)
	binary.BigEndian.PutUint64(next, generation)
	if err := generations.Put(globalKey, next); err != nil {
		return 0, err
	}
	reservation := make([]byte, 8)
	binary.BigEndian.PutUint64(reservation, generation)
	if err := reservations.Put([]byte(id), reservation); err != nil {
		return 0, err
	}
	return generation, nil
}

// ReserveOutboxGenerationIfUnchanged atomically revalidates an active-row
// snapshot and reserves the next generation. Migration callers must retry when
// the snapshot changed.
func (se *StorageEngine) ReserveOutboxGenerationIfUnchanged(
	id string,
	expected []byte,
) (uint64, bool, error) {
	var generation uint64
	reserved := false
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket([]byte(bucketNotifyOutboxV2))
		reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
		if entries == nil || reservations == nil {
			return fmt.Errorf("outbox v2 bucket not found")
		}
		current := entries.Get([]byte(id))
		if (current == nil) != (expected == nil) || !bytes.Equal(current, expected) {
			return nil
		}
		if reservations.Get([]byte(id)) != nil {
			return nil
		}
		var err error
		generation, err = reserveOutboxGenerationTx(tx, id)
		reserved = err == nil
		return err
	})
	return generation, reserved, err
}

func (se *StorageEngine) GetOutboxRaw(id string) ([]byte, error) {
	var out []byte
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutboxV2))
		if b == nil {
			return fmt.Errorf("notify_outbox_v2 bucket not found")
		}
		if raw := b.Get([]byte(id)); raw != nil {
			out = bytes.Clone(raw)
		}
		return nil
	})
	return out, err
}

// ReleaseOutboxGeneration reclaims a failed staging reservation without
// disturbing a newer reservation for the same entity.
func (se *StorageEngine) ReleaseOutboxGeneration(id string, generation uint64) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
		if b == nil {
			return fmt.Errorf("outbox v2 reservations bucket not found")
		}
		raw := b.Get([]byte(id))
		if len(raw) != 8 || binary.BigEndian.Uint64(raw) != generation {
			return nil
		}
		return b.Delete([]byte(id))
	})
}

// PutOutboxRawIfCurrentGeneration commits data only while generation is still
// the newest reservation. Payload-verified legacy rows are removed atomically.
func (se *StorageEngine) PutOutboxRawIfCurrentGeneration(
	id string,
	generation uint64,
	data []byte,
	superseded map[string][]byte,
) (bool, error) {
	applied := false
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket([]byte(bucketNotifyOutboxV2))
		reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
		legacy := tx.Bucket([]byte(bucketNotifyOutbox))
		if entries == nil || reservations == nil || legacy == nil {
			return fmt.Errorf("outbox bucket not found")
		}
		rawGeneration := reservations.Get([]byte(id))
		if len(rawGeneration) != 8 || binary.BigEndian.Uint64(rawGeneration) != generation {
			return nil
		}
		for supersededID, expected := range superseded {
			current := legacy.Get([]byte(supersededID))
			if current != nil && bytes.Equal(current, expected) {
				if err := legacy.Delete([]byte(supersededID)); err != nil {
					return err
				}
			}
		}
		if err := entries.Put([]byte(id), data); err != nil {
			return err
		}
		if err := reservations.Delete([]byte(id)); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// PutOutboxRaw upserts a legacy/raw notify outbox entry.
func (se *StorageEngine) PutOutboxRaw(id string, data []byte) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return fmt.Errorf("notify_outbox bucket not found")
		}
		return b.Put([]byte(id), data)
	})
}

func (se *StorageEngine) DeleteOutboxEntry(id string) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket([]byte(bucketNotifyOutboxV2))
		reservations := tx.Bucket([]byte(bucketNotifyOutboxV2Reservations))
		if entries == nil || reservations == nil {
			return fmt.Errorf("outbox v2 bucket not found")
		}
		if err := entries.Delete([]byte(id)); err != nil {
			return err
		}
		return reservations.Delete([]byte(id))
	})
}

// DeleteOutboxEntryIfUnchanged acknowledges only the exact generation read by
// a sender. A same-key replacement remains durable for a later attempt.
func (se *StorageEngine) DeleteOutboxEntryIfUnchanged(id string, expected []byte) (bool, error) {
	deleted := false
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutboxV2))
		if b == nil {
			return fmt.Errorf("notify_outbox_v2 bucket not found")
		}
		current := b.Get([]byte(id))
		if current == nil || !bytes.Equal(current, expected) {
			return nil
		}
		if err := b.Delete([]byte(id)); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, err
}

func (se *StorageEngine) OutboxEntryMatches(id string, expected []byte) (bool, error) {
	matches := false
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutboxV2))
		if b == nil {
			return fmt.Errorf("notify_outbox_v2 bucket not found")
		}
		matches = bytes.Equal(b.Get([]byte(id)), expected)
		return nil
	})
	return matches, err
}

func (se *StorageEngine) CountOutboxEntries() (int, error) {
	var n int
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		v2 := tx.Bucket([]byte(bucketNotifyOutboxV2))
		legacy := tx.Bucket([]byte(bucketNotifyOutbox))
		if v2 == nil || legacy == nil {
			return fmt.Errorf("outbox bucket not found")
		}
		n = v2.Stats().KeyN
		return legacy.ForEach(func(k, v []byte) error {
			if !isRecognizedLegacyOutboxMetadata(k, v) {
				n++
			}
			return nil
		})
	})
	return n, err
}

func (se *StorageEngine) ListOutboxRaw() (map[string][]byte, error) {
	out := make(map[string][]byte)
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutboxV2))
		if b == nil {
			return fmt.Errorf("notify_outbox_v2 bucket not found")
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

// ListLegacyOutboxRaw returns only rows that still require payload-aware
// reconciliation. Recognized metadata is never exposed as a notification.
func (se *StorageEngine) ListLegacyOutboxRaw() (map[string][]byte, error) {
	out := make(map[string][]byte)
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return fmt.Errorf("notify_outbox bucket not found")
		}
		return b.ForEach(func(k, v []byte) error {
			if isRecognizedLegacyOutboxMetadata(k, v) {
				return nil
			}
			out[string(k)] = bytes.Clone(v)
			return nil
		})
	})
	return out, err
}

func (se *StorageEngine) DeleteLegacyOutboxEntriesIfUnchanged(entries map[string][]byte) error {
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotifyOutbox))
		if b == nil {
			return fmt.Errorf("notify_outbox bucket not found")
		}
		for id, expected := range entries {
			if current := b.Get([]byte(id)); current != nil && bytes.Equal(current, expected) {
				if err := b.Delete([]byte(id)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (se *StorageEngine) requestRemoteDownload(file protocol.IndexEntry, source string) error {
	intent := downloadIntent{File: file, Source: source}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	key := downloadIntentStorageKey(file.Name)
	se.mutationMu.Lock()
	err = se.subscriptions.Update(func(tx *bbolt.Tx) error {
		current, exists, err := boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, file.Name)
		if err != nil {
			return err
		}
		if !exists || current != file || current.Deleted {
			return ErrBlobDiscarded
		}
		subscribed, err := boltHasKey(tx, bucketSubscriptions, file.Name)
		if err != nil {
			return err
		}
		if !subscribed {
			return ErrBlobDiscarded
		}
		b := tx.Bucket([]byte(bucketDownloadIntents))
		if b == nil {
			return fmt.Errorf("download_intents bucket not found")
		}
		return b.Put([]byte(key), raw)
	})
	se.mutationMu.Unlock()
	if err != nil {
		if errors.Is(err, ErrBlobDiscarded) {
			return err
		}
		return fmt.Errorf("persist download intent: %w", err)
	}
	token, startCallback := se.reserveDownload(key, intent)
	if !startCallback {
		return nil
	}
	return se.invokeDownloadCallback(key, intent, token)
}

func (se *StorageEngine) enqueueDownloadIntent(key string, intent downloadIntent) error {
	token, startCallback := se.reserveDownload(key, intent)
	if !startCallback {
		return nil
	}
	return se.invokeDownloadCallback(key, intent, token)
}

func (se *StorageEngine) reserveDownload(key string, intent downloadIntent) (uint64, bool) {
	se.downloadMu.Lock()
	defer se.downloadMu.Unlock()
	if active, ok := se.activeDownloads[key]; ok && active.Intent.File == intent.File {
		return active.Token, false
	}
	se.nextDownloadID++
	if se.nextDownloadID == 0 {
		se.nextDownloadID++
	}
	token := se.nextDownloadID
	se.activeDownloads[key] = activeDownload{Intent: intent, Token: token}
	return token, true
}

func (se *StorageEngine) invokeDownloadCallback(key string, intent downloadIntent, token uint64) error {
	if se.onDownloadNeeded == nil {
		err := fmt.Errorf("download callback unavailable")
		se.rollbackDownloadReservation(key, token)
		return err
	}
	if err := se.onDownloadNeeded(intent.File, intent.Source); err != nil {
		se.rollbackDownloadReservation(key, token)
		return err
	}
	return nil
}

func (se *StorageEngine) rollbackDownloadReservation(key string, token uint64) {
	se.downloadMu.Lock()
	if active, ok := se.activeDownloads[key]; ok && active.Token == token {
		delete(se.activeDownloads, key)
	}
	se.downloadMu.Unlock()
}

func (se *StorageEngine) listDownloadIntents() (map[string]downloadIntent, error) {
	intents := make(map[string]downloadIntent)
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDownloadIntents))
		if b == nil {
			return fmt.Errorf("download_intents bucket not found")
		}
		return b.ForEach(func(k, v []byte) error {
			var intent downloadIntent
			if err := json.Unmarshal(v, &intent); err != nil {
				return fmt.Errorf("invalid download intent %q: %w", string(k), err)
			}
			intents[string(k)] = intent
			return nil
		})
	})
	return intents, err
}

func (se *StorageEngine) ReconcileDownloadIntents() error {
	intents, err := se.listDownloadIntents()
	if err != nil {
		return err
	}
	var firstErr error
	for key, intent := range intents {
		current, exists, err := se.GetFileMetaE(intent.File.Name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		subscribed := false
		if exists && current == intent.File && !current.Deleted {
			subscribed, err = se.IsSubscribedE(current.Name)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if !exists || current != intent.File || current.Deleted || !subscribed {
			if err := se.completeDownloadIntent(intent.File); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		hasBlob, err := se.HasPhysicalBlob(current.Hash)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if hasBlob {
			if err := se.completeDownloadIntent(intent.File); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := se.enqueueDownloadIntent(key, intent); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (se *StorageEngine) CountDownloadIntents() int {
	count, err := se.CountDownloadIntentsE()
	if err != nil {
		se.logger.Error("Failed to count durable download intents", "error", err)
		return 0
	}
	return count
}

func (se *StorageEngine) CountDownloadIntentsE() (int, error) {
	intents, err := se.listDownloadIntents()
	if err != nil {
		return 0, err
	}
	return len(intents), nil
}

func (se *StorageEngine) releaseDownloadIntent(file protocol.IndexEntry) {
	key := downloadIntentStorageKey(file.Name)
	se.downloadMu.Lock()
	if active, ok := se.activeDownloads[key]; ok && active.Intent.File == file {
		delete(se.activeDownloads, key)
	}
	se.downloadMu.Unlock()
}

// ReleaseDownloadAttempt clears only the process-local active reservation.
// The durable intent remains for worker/startup reconciliation.
func (se *StorageEngine) ReleaseDownloadAttempt(file protocol.IndexEntry) {
	se.releaseDownloadIntent(file)
}

// QuarantineCorruptDownload removes only the durable intent for this exact
// metadata revision. A later manifest/source can persist a fresh intent.
func (se *StorageEngine) QuarantineCorruptDownload(file protocol.IndexEntry) error {
	return se.completeDownloadIntent(file)
}

func (se *StorageEngine) completeDownloadIntent(file protocol.IndexEntry) error {
	key := downloadIntentStorageKey(file.Name)
	err := se.subscriptions.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDownloadIntents))
		if b == nil {
			return fmt.Errorf("download_intents bucket not found")
		}
		raw := b.Get([]byte(key))
		if raw == nil {
			return nil
		}
		var intent downloadIntent
		if err := json.Unmarshal(raw, &intent); err != nil {
			return fmt.Errorf("invalid download intent %q: %w", key, err)
		}
		if intent.File != file {
			return nil
		}
		return b.Delete([]byte(key))
	})
	if err != nil {
		return err
	}
	se.releaseDownloadIntent(file)
	return nil
}
