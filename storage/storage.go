package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"errors"
)

type Storage struct {
	baseDir string
}

func Map[T, U any](slice []T, fn func(T) U) []U {
	result := make([]U, len(slice))
	for i, v := range slice {
		result[i] = fn(v)
	}
	return result
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
	defer os.Remove(tempName)
	hasher := sha256.New()
	mw := io.MultiWriter(file, hasher)
	writtenBytes, err := io.Copy(mw, content)
	if err != nil {
		file.Close()
		return "", 0, err
	}
	generatedHash := hex.EncodeToString(hasher.Sum(nil))
	fullpath := filepath.Join(st.baseDir, generatedHash)
	_, err = os.Stat(fullpath)
	defer file.Close()
	if os.IsNotExist(err){
		err = os.Rename(file.Name(), fullpath)	
		if err != nil {
			return "", 0, err
		}
	}
	return generatedHash, writtenBytes, nil
}

func ReadFileFromClient(clientIP string, pathToRead string) (io.ReadCloser, error) {
	// For now this is mocked. It should do a HTTP GET request to the client.
	return os.Open(pathToRead)
}

func (st *Storage) AmountOfBlobs() (error, int) {
	result := 0
	err := VisitAndDo(st, func(string, fs.DirEntry) error { result++; return nil },
		IsNotADir)

	if err != nil {
		return err, 0
	}
	return nil, result
}

func (st *Storage) BlobExists(hash string) (bool, error) {
	fullPath := filepath.Join(st.baseDir, hash)
	if _, err := os.Stat(fullPath); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

func (st *Storage) ReadBlob(hash string, w io.Writer) error {
	fullPath := filepath.Join(st.baseDir, hash)

	file, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		return ErrFileDoesNotExist
	}
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}

func (st *Storage) DeleteBlob(hash string) error {
	fullPath := filepath.Join(st.baseDir, hash)
	return os.Remove(fullPath)
}
