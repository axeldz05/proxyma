package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func NewStorage(baseDir string) *Storage {
	st := &Storage{
		baseDir:   baseDir,
		blobCache: make(map[string]bool),
		verified:  make(map[string]blobFingerprint),
		hashLocks: make(map[string]*hashLock),
	}
	st.populateCache()
	return st
}

type Storage struct {
	baseDir     string
	mu          sync.RWMutex
	blobCache   map[string]bool
	verified    map[string]blobFingerprint
	hashLocksMu sync.Mutex
	hashLocks   map[string]*hashLock
}

type hashLock struct {
	mu   sync.Mutex
	refs int
}

type blobFingerprint struct {
	size    int64
	modTime int64
}

func (st *Storage) lockHash(hash string) func() {
	st.hashLocksMu.Lock()
	lock := st.hashLocks[hash]
	if lock == nil {
		lock = &hashLock{}
		st.hashLocks[hash] = lock
	}
	lock.refs++
	st.hashLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		st.hashLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(st.hashLocks, hash)
		}
		st.hashLocksMu.Unlock()
	}
}

func isHexString(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// IsValidCASHash reports whether s is a 64-char hex SHA-256 digest (L1).
func IsValidCASHash(s string) bool {
	return len(s) == 64 && isHexString(s)
}

func (st *Storage) populateCache() {
	st.mu.Lock()
	defer st.mu.Unlock()
	_ = VisitAndDo(st, func(path string, d fs.DirEntry) error {
		name := d.Name()
		if IsValidCASHash(name) {
			st.blobCache[name] = true
		}
		return nil
	}, IsNotADir)
}

func (st *Storage) Name() string {
	return filepath.Base(st.baseDir)
}

func (st *Storage) SaveBlob(content io.Reader) (string, int64, error) {
	hash, size, _, err := st.SaveBlobWithStatus(content)
	return hash, size, err
}

// SaveBlobWithStatus stores content without replacing an existing CAS object.
// created is true only when this call atomically created the final hash path.
func (st *Storage) SaveBlobWithStatus(content io.Reader) (hash string, size int64, created bool, err error) {
	staged, err := st.StageBlob(content)
	if err != nil {
		return "", 0, false, err
	}
	defer func() { _ = staged.Discard() }()
	if err := staged.Prepare(); err != nil {
		return "", 0, false, err
	}
	created, err = staged.Commit()
	if err != nil {
		return "", 0, false, err
	}
	return staged.Hash(), staged.Size(), created, err
}

// StagedBlob contains fully written and fsynced content that has not yet been
// linked into the CAS namespace. Large reader I/O therefore happens before a
// caller enters its metadata critical section.
type StagedBlob struct {
	storage  *Storage
	tempPath string
	hash     string
	size     int64
	mu       sync.Mutex
	done     bool
}

func (st *Storage) StageBlob(content io.Reader) (*StagedBlob, error) {
	file, err := os.CreateTemp(st.baseDir, "tmp-blob-*")
	if err != nil {
		return nil, err
	}
	tempName := file.Name()
	hasher := sha256.New()
	mw := io.MultiWriter(file, hasher)
	writtenBytes, err := io.Copy(mw, content)
	if err != nil {
		closeErr := file.Close()
		_ = os.Remove(tempName)
		return nil, errors.Join(err, closeErr)
	}
	generatedHash := hex.EncodeToString(hasher.Sum(nil))
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("sync temporary blob: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("failed to close file safely: %w", err)
	}
	return &StagedBlob{
		storage:  st,
		tempPath: tempName,
		hash:     generatedHash,
		size:     writtenBytes,
	}, nil
}

func (b *StagedBlob) Hash() string {
	return b.hash
}

func (b *StagedBlob) Size() int64 {
	return b.size
}

// Prepare performs any large verification of an existing CAS object before a
// caller enters its metadata critical section. Commit rechecks a cheap file
// fingerprint under the per-hash lock and only rehashes if external code
// changed the path between Prepare and Commit.
func (b *StagedBlob) Prepare() error {
	unlock := b.storage.lockHash(b.hash)
	defer unlock()
	fullpath := filepath.Join(b.storage.baseDir, b.hash)
	if b.storage.verifiedCurrentLocked(b.hash) {
		return nil
	}
	info, err := os.Lstat(fullpath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := b.storage.verifyAndCacheLocked(b.hash); err != nil {
		if !errors.Is(err, ErrBlobCorrupt) {
			return err
		}
		repairable := info.Mode().IsRegular()
		if quarantineErr := b.storage.quarantineBlobLocked(b.hash); quarantineErr != nil {
			return errors.Join(err, quarantineErr)
		}
		if !repairable {
			return err
		}
	}
	return nil
}

func (b *StagedBlob) Commit() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return false, fmt.Errorf("staged blob already finalized")
	}
	unlock := b.storage.lockHash(b.hash)
	defer unlock()

	fullpath := filepath.Join(b.storage.baseDir, b.hash)
	created, err := b.commitLocked(fullpath)
	if err != nil {
		return false, err
	}
	b.storage.mu.Lock()
	b.storage.blobCache[b.hash] = true
	b.storage.mu.Unlock()
	b.done = true
	_ = os.Remove(b.tempPath)
	return created, nil
}

func (b *StagedBlob) commitLocked(fullpath string) (bool, error) {
	if linkErr := os.Link(b.tempPath, fullpath); linkErr != nil {
		if _, statErr := os.Stat(fullpath); statErr != nil {
			return false, fmt.Errorf("create blob destination: %w", linkErr)
		}
		if !b.storage.verifiedCurrentLocked(b.hash) {
			err := b.storage.verifyAndCacheLocked(b.hash)
			if err == nil {
				return false, nil
			}
			if !errors.Is(err, ErrBlobCorrupt) {
				return false, fmt.Errorf("existing CAS verification failed: %w", err)
			}
			info, statErr := os.Lstat(fullpath)
			repairable := statErr == nil && info.Mode().IsRegular()
			if quarantineErr := b.storage.quarantineBlobLocked(b.hash); quarantineErr != nil {
				return false, errors.Join(
					fmt.Errorf("existing CAS verification failed: %w", err),
					fmt.Errorf("quarantine corrupt CAS blob: %w", quarantineErr),
				)
			}
			if !repairable {
				return false, fmt.Errorf("existing CAS verification failed: %w", err)
			}
			if retryErr := os.Link(b.tempPath, fullpath); retryErr != nil {
				return false, fmt.Errorf("replace quarantined CAS blob: %w", retryErr)
			}
			if err := syncDirectory(b.storage.baseDir); err != nil {
				_ = os.Remove(fullpath)
				return false, fmt.Errorf("sync repaired blob directory: %w", err)
			}
			b.storage.cacheVerifiedLocked(b.hash)
			return true, nil
		}
		return false, nil
	}
	if err := syncDirectory(b.storage.baseDir); err != nil {
		_ = os.Remove(fullpath)
		return false, fmt.Errorf("sync blob directory: %w", err)
	}
	b.storage.cacheVerifiedLocked(b.hash)
	return true, nil
}

func (b *StagedBlob) Discard() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return nil
	}
	b.done = true
	err := os.Remove(b.tempPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func verifyCASFile(path, expectedHash string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.Join(ErrBlobCorrupt, fmt.Errorf("CAS path is not a regular file"))
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.Join(ErrBlobCorrupt, fmt.Errorf("CAS path changed while verifying"))
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != expectedHash {
		return errors.Join(
			ErrBlobCorrupt,
			fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actualHash),
		)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (st *Storage) BlobExists(hash string) (bool, error) {
	if !IsValidCASHash(hash) {
		return false, nil
	}
	unlock := st.lockHash(hash)
	defer unlock()
	if err := st.verifyAndCacheLocked(hash); err != nil {
		if os.IsNotExist(err) {
			st.removeCachedHash(hash)
			return false, nil
		}
		if errors.Is(err, ErrBlobCorrupt) {
			quarantineErr := st.quarantineBlobLocked(hash)
			return false, errors.Join(err, quarantineErr)
		}
		return false, err
	}
	return true, nil
}

func (st *Storage) ReadBlob(hash string, w io.Writer) error {
	if !IsValidCASHash(hash) {
		return ErrFileDoesNotExist
	}
	unlock := st.lockHash(hash)
	defer unlock()
	fullPath := filepath.Join(st.baseDir, hash)
	if err := st.verifyAndCacheLocked(hash); err != nil {
		if os.IsNotExist(err) {
			st.removeCachedHash(hash)
			return ErrFileDoesNotExist
		}
		if errors.Is(err, ErrBlobCorrupt) {
			quarantineErr := st.quarantineBlobLocked(hash)
			return errors.Join(err, quarantineErr)
		}
		return err
	}
	file, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		return ErrFileDoesNotExist
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.Copy(w, file)
	return err
}

func (st *Storage) DeleteBlob(hash string) error {
	unlock := st.lockHash(hash)
	defer unlock()
	fullPath := filepath.Join(st.baseDir, hash)
	err := os.Remove(fullPath)
	if err == nil || os.IsNotExist(err) {
		st.removeCachedHash(hash)
		return nil
	}
	return err
}

func (st *Storage) removeCachedHash(hash string) {
	st.mu.Lock()
	delete(st.blobCache, hash)
	delete(st.verified, hash)
	st.mu.Unlock()
}

func fingerprint(info os.FileInfo) blobFingerprint {
	return blobFingerprint{size: info.Size(), modTime: info.ModTime().UnixNano()}
}

func (st *Storage) verifiedCurrentLocked(hash string) bool {
	info, err := os.Lstat(st.GetBlobPath(hash))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	st.mu.RLock()
	expected, ok := st.verified[hash]
	st.mu.RUnlock()
	return ok && expected == fingerprint(info)
}

func (st *Storage) verifyAndCacheLocked(hash string) error {
	path := st.GetBlobPath(hash)
	if err := verifyCASFile(path, hash); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st.mu.Lock()
	st.blobCache[hash] = true
	st.verified[hash] = fingerprint(info)
	st.mu.Unlock()
	return nil
}

func (st *Storage) cacheVerifiedLocked(hash string) {
	info, err := os.Lstat(st.GetBlobPath(hash))
	if err != nil {
		return
	}
	st.mu.Lock()
	st.blobCache[hash] = true
	st.verified[hash] = fingerprint(info)
	st.mu.Unlock()
}

func (st *Storage) quarantineBlobLocked(hash string) error {
	source := st.GetBlobPath(hash)
	quarantine := filepath.Join(st.baseDir, ".corrupt-"+hash)
	if err := os.RemoveAll(quarantine); err != nil {
		return err
	}
	if err := os.Rename(source, quarantine); err != nil {
		if os.IsNotExist(err) {
			st.removeCachedHash(hash)
			return nil
		}
		return err
	}
	st.removeCachedHash(hash)
	return syncDirectory(st.baseDir)
}

func (st *Storage) GetBlobPath(hash string) string {
	return filepath.Join(st.baseDir, hash)
}

func (st *Storage) CleanupTempFiles() {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.baseDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "tmp-blob-") {
			info, err := entry.Info()
			if err == nil && time.Since(info.ModTime()) > 30*time.Minute {
				_ = os.Remove(filepath.Join(st.baseDir, entry.Name()))
			}
		}
	}
}
