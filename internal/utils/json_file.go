package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadJSONFile decodes a JSON file into dest (L1).
func ReadJSONFile(path string, dest any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewDecoder(f).Decode(dest)
}

// WriteJSONFile atomically encodes v as indented JSON to path (L1).
func WriteJSONFile(path string, v any) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat JSON destination: %w", err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	closeWithError := func(cause error) error {
		if closeErr := f.Close(); closeErr != nil && cause == nil {
			return closeErr
		}
		return cause
	}

	if err := f.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return closeWithError(err)
	}
	if err := f.Sync(); err != nil {
		return closeWithError(fmt.Errorf("sync JSON temporary file: %w", err))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close JSON temporary file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace JSON file: %w", err)
	}

	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open JSON parent directory: %w", err)
	}
	defer func() { _ = dirHandle.Close() }()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync JSON parent directory: %w", err)
	}
	return nil
}
