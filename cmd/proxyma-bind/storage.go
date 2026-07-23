package proxyma_bind

import (
	"fmt"
	"os"
	"path/filepath"

	"proxyma/internal/server"
)

func GetVFSFilesJson() string {
	return dispatchUnixOrLocal("vfs_list", nil, func(s *server.Server) (any, error) {
		return s.LocalVFSList(), nil
	})
}

// SyncVFS triggers VFS synchronization.
func SyncVFS() string {
	return dispatchUnixOrLocal("sync", nil, func(s *server.Server) (any, error) {
		err := s.ExecuteSync()
		if err != nil {
			return nil, err
		}
		return "", nil
	})
}

// UploadFile uploads a local file to the node's VFS.
func UploadFile(name string, filePath string) string {
	return dispatchUnixOrLocal("vfs_upload", map[string]string{
		"path": filePath,
		"name": name,
	}, func(s *server.Server) (any, error) {
		f, err := os.Open(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
		}
		defer func() { _ = f.Close() }()

		err = s.Storage.SaveLocalFile(name, f)
		if err != nil {
			return nil, err
		}
		return "", nil
	})
}

// SetSubscription enables/disables subscription for a VFS file.
func SetSubscription(name string, subscribe bool) string {
	action := "vfs_subscribe"
	if !subscribe {
		action = "vfs_unsubscribe"
	}
	return dispatchUnixOrLocal(action, map[string]string{
		"name": name,
	}, func(s *server.Server) (any, error) {
		s.Storage.SetSubscription(name, subscribe)
		if subscribe {
			go func() {
				_ = s.ExecuteSync()
			}()
		}
		return "", nil
	})
}

// DeleteLocalCache deletes the local blob copy of a VFS file.
func DeleteLocalCache(name string) string {
	return dispatchUnixOrLocal("vfs_purge", map[string]string{
		"name": name,
	}, func(s *server.Server) (any, error) {
		err := s.Storage.DeleteLocalCache(name)
		if err != nil {
			return nil, err
		}
		return "", nil
	})
}

// DeleteFile marks a VFS file as deleted in the registry.
func DeleteFile(name string) string {
	return dispatchUnixOrLocal("vfs_delete", map[string]string{
		"name": name,
	}, func(s *server.Server) (any, error) {
		err := s.Storage.DeleteLocalFile(name)
		if err != nil {
			return nil, err
		}
		return "", nil
	})
}

// GetLocalBlobPath returns absolute local file path for open operations.
func GetLocalBlobPath(hash string) string {
	s := getSrv()

	if s == nil {
		return filepath.Join(appStorage, "blobs", hash)
	}
	return s.Storage.GetLocalBlobPath(hash)
}

// FetchFileOnDemand downloads an unsubscribed or missing file on demand from peers into local cache.
func FetchFileOnDemand(name string) string {
	return dispatchUnixOrLocal("vfs_fetch", map[string]string{
		"name": name,
	}, func(s *server.Server) (any, error) {
		err := s.FetchFileOnDemand(name)
		if err != nil {
			return nil, err
		}
		return "", nil
	})
}
