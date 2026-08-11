package storage

import (
	"crypto/sha256"
	"encoding/hex"
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
	}
	st.populateCache()
	return st
}

type Storage struct {
	baseDir   string
	mu        sync.RWMutex
	blobCache map[string]bool
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
	file, err := os.CreateTemp(st.baseDir, "tmp-blob-*")
	if err != nil {
		return "", 0, err
	}
	tempName := file.Name()
	defer func() { _ = os.Remove(tempName) }()
	hasher := sha256.New()
	mw := io.MultiWriter(file, hasher)
	writtenBytes, err := io.Copy(mw, content)
	if err != nil {
		if err := file.Close(); err != nil {
			return "", 0, fmt.Errorf("failed to close file safely: %w", err)
		}
		return "", 0, err
	}
	generatedHash := hex.EncodeToString(hasher.Sum(nil))
	fullpath := filepath.Join(st.baseDir, generatedHash)
	fi, err := os.Stat(fullpath)
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("failed to close file safely: %w", err)
	}

	if err != nil && !os.IsNotExist(err) {
		return "", 0, fmt.Errorf("stat blob destination: %w", err)
	}
	if err == nil {
		if fi.IsDir() {
			return "", 0, fmt.Errorf("blob destination is a directory: %s", generatedHash)
		}
		// CAS object already present — idempotent success.
	} else {
		if renameErr := os.Rename(file.Name(), fullpath); renameErr != nil {
			// Concurrent SaveBlob of same content may have won the rename.
			if _, statErr := os.Stat(fullpath); statErr != nil {
				return "", 0, renameErr
			}
		}
	}

	st.mu.Lock()
	st.blobCache[generatedHash] = true
	st.mu.Unlock()

	return generatedHash, writtenBytes, nil
}

func (st *Storage) BlobExists(hash string) (bool, error) {
	st.mu.RLock()
	exists := st.blobCache[hash]
	st.mu.RUnlock()
	if !exists {
		return false, nil
	}
	_, err := os.Stat(st.GetBlobPath(hash))
	if err == nil {
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	st.mu.Lock()
	delete(st.blobCache, hash)
	st.mu.Unlock()
	return false, nil
}

func (st *Storage) ReadBlob(hash string, w io.Writer) error {
	if !IsValidCASHash(hash) {
		return ErrFileDoesNotExist
	}
	fullPath := filepath.Join(st.baseDir, hash)

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
	fullPath := filepath.Join(st.baseDir, hash)
	err := os.Remove(fullPath)
	if err == nil || os.IsNotExist(err) {
		st.mu.Lock()
		delete(st.blobCache, hash)
		st.mu.Unlock()
		return nil
	}
	return err
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
