package storage

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"time"
)

func RandomName(fm *FileManager, path string) (string, error) {
	var newName string

	folderPath := filepath.Dir(path)
	if folderPath != "." && folderPath != ".." {
		folderPath, err := fm.LookUpAbsoluteFolderPath(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		folderPath, err = filepath.Rel(fm.baseDir, folderPath)
		if err != nil {
			return "", err
		}
		newName = folderPath
	}
	b := make([]byte, 8) // 8 bytes → 16 hex chars
	if _, err := rand.Read(b); err != nil {
		// fallback to timestamp if crypto/rand fails
		return time.Now().UTC().Format("20060102-150405.000000"), nil
	}
	newName = filepath.Join(newName, hex.EncodeToString(b))
	return newName, nil
}
