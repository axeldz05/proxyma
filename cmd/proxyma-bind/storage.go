package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"proxyma/internal/protocol"
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
		if err := s.LocalVFSUpload(name, filePath); err != nil {
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
		if err := s.LocalVFSSubscribe(name, subscribe); err != nil {
			return nil, err
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
		return filepath.Join(appStorage, hash)
	}
	return s.Storage.GetBlobPath(hash)
}

// ResolveLocalBlob fetches a VFS file on demand and returns its local blob path (L2).
// On failure returns BindErrorJSON.
func ResolveLocalBlob(name string) string {
	if name == "" {
		return BindErrorJSON(fmt.Errorf("missing name parameter"))
	}
	if errStr := FetchFileOnDemand(name); IsBindError(errStr) {
		return errStr
	}
	filesJSON := GetVFSFilesJson()
	if IsBindError(filesJSON) {
		return filesJSON
	}
	var files []protocol.VFSFileStatus
	if err := json.Unmarshal([]byte(filesJSON), &files); err != nil {
		return BindErrorJSON(fmt.Errorf("failed to parse VFS list: %w", err))
	}
	var hash string
	for _, f := range files {
		if f.Name == name {
			hash = f.Hash
			break
		}
	}
	if hash == "" {
		return BindErrorJSON(fmt.Errorf("file '%s' not found in VFS topology", name))
	}
	return GetLocalBlobPath(hash)
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
