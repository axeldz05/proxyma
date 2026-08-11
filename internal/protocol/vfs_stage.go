package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RewriteLocalFilePaths stages local file path values in m via stage and rewrites them to vfs:// URIs (L2).
// Recurses into nested map[string]any and []any values. When annotateOutputs is true, also sets
// OutputHashKey / OutputNameKey / OutputSizeKey scoped per parameter key (and nested maps).
// Returns an error if staging fails, returns an empty hash, or a path that looks local but cannot be staged.
func RewriteLocalFilePaths(m map[string]any, stage func(path string) (hash string, size int64, err error), annotateOutputs bool) error {
	if m == nil || stage == nil {
		return nil
	}
	for k, v := range m {
		switch typed := v.(type) {
		case map[string]any:
			if err := RewriteLocalFilePaths(typed, stage, annotateOutputs); err != nil {
				return err
			}
			continue
		case []any:
			if err := rewriteLocalFileSlice(typed, stage, annotateOutputs); err != nil {
				return err
			}
			continue
		}
		pathStr, ok := IsStageableLocalPath(v)
		if !ok {
			continue
		}
		fi, err := os.Stat(pathStr)
		if err != nil {
			if looksLikeFilesystemPath(pathStr) {
				return fmt.Errorf("stage local path %q: %w", pathStr, err)
			}
			continue
		}
		if fi.IsDir() {
			return fmt.Errorf("stage local path %q: is a directory", pathStr)
		}
		hash, size, err := stage(pathStr)
		if err != nil {
			return fmt.Errorf("stage local path %q: %w", pathStr, err)
		}
		if hash == "" {
			return fmt.Errorf("stage local path %q returned empty hash", pathStr)
		}
		if annotateOutputs {
			annotateStagedOutput(m, k, hash, pathStr, size)
		}
		m[k] = VFSURI(hash)
	}
	return nil
}

func rewriteLocalFileSlice(items []any, stage func(path string) (hash string, size int64, err error), annotateOutputs bool) error {
	for i, v := range items {
		switch typed := v.(type) {
		case map[string]any:
			if err := RewriteLocalFilePaths(typed, stage, annotateOutputs); err != nil {
				return err
			}
		case []any:
			if err := rewriteLocalFileSlice(typed, stage, annotateOutputs); err != nil {
				return err
			}
		default:
			pathStr, ok := IsStageableLocalPath(v)
			if !ok {
				continue
			}
			fi, err := os.Stat(pathStr)
			if err != nil {
				if looksLikeFilesystemPath(pathStr) {
					return fmt.Errorf("stage local path %q: %w", pathStr, err)
				}
				continue
			}
			if fi.IsDir() {
				return fmt.Errorf("stage local path %q: is a directory", pathStr)
			}
			hash, size, err := stage(pathStr)
			if err != nil {
				return fmt.Errorf("stage local path %q: %w", pathStr, err)
			}
			if hash == "" {
				return fmt.Errorf("stage local path %q returned empty hash", pathStr)
			}
			_ = size
			items[i] = VFSURI(hash)
		}
	}
	return nil
}

func annotateStagedOutput(m map[string]any, key, hash, pathStr string, size int64) {
	// Prefer per-key metadata so multi-file outputs do not clobber each other.
	m[key+"_"+OutputHashKey] = hash
	m[key+"_"+OutputNameKey] = filepath.Base(pathStr)
	m[key+"_"+OutputSizeKey] = float64(size)
	// Keep legacy global keys for the first annotated file only.
	if _, exists := m[OutputHashKey]; !exists {
		m[OutputHashKey] = hash
		m[OutputNameKey] = filepath.Base(pathStr)
		m[OutputSizeKey] = float64(size)
	}
}

func looksLikeFilesystemPath(s string) bool {
	if s == "" || IsVFSURI(s) {
		return false
	}
	return strings.Contains(s, "/") || strings.Contains(s, `\`) || filepath.IsAbs(s)
}
