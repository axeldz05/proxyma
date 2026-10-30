package protocol

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultStorageDirName is the canonical global user directory name (~/.proxyma).
	DefaultStorageDirName = ".proxyma"

	// EnvStorage overrides the storage directory for all commands.
	EnvStorage = "PROXYMA_STORAGE"
)

// ExpandHome expands leading ~/ or ~ in path to the current user's home directory.
func ExpandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// DefaultUserHomeStorage returns the canonical global storage directory (~/.proxyma).
func DefaultUserHomeStorage() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "./data"
	}
	return filepath.Join(home, DefaultStorageDirName)
}

// ResolveActiveStorage calculates the active storage directory according to precedence:
// 1. Explicit --storage flag (when explicitly passed on the command line)
// 2. PROXYMA_STORAGE environment variable
// 3. DefaultUserHomeStorage (~/.proxyma)
func ResolveActiveStorage(explicitStorage string, storageChanged bool) string {
	if storageChanged && explicitStorage != "" {
		return ExpandHome(filepath.Clean(explicitStorage))
	}
	if envStorage := strings.TrimSpace(os.Getenv(EnvStorage)); envStorage != "" {
		return ExpandHome(filepath.Clean(envStorage))
	}
	return ExpandHome(filepath.Clean(DefaultUserHomeStorage()))
}

// DefaultStoragePath returns the storage path for the current environment or canonical user default (~/.proxyma).
func DefaultStoragePath() string {
	return ResolveActiveStorage("", false)
}
