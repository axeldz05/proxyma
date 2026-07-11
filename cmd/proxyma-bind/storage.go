package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func GetVFSFilesJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()
	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "vfs_list", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list := s.LocalVFSList()
	b, _ := json.Marshal(list)
	return string(b)
}

// SyncVFS triggers VFS synchronization.
func SyncVFS() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "sync", nil)
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.ExecuteSync()
	if err != nil {
		return err.Error()
	}
	return ""
}

// UploadFile uploads a local file to the node's VFS.
func UploadFile(name string, filePath string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_upload", map[string]string{
			"path": filePath,
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Sprintf("failed to open file %s: %v", filePath, err)
	}
	defer f.Close()

	err = s.Storage.SaveLocalFile(name, f)
	if err != nil {
		return err.Error()
	}
	return ""
}

// SetSubscription enables/disables subscription for a VFS file.
func SetSubscription(name string, subscribe bool) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		action := "vfs_subscribe"
		if !subscribe {
			action = "vfs_unsubscribe"
		}
		_, err := sendUnixSocketCommand(appStorage, action, map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	s.Storage.SetSubscription(name, subscribe)
	if subscribe {
		go func() {
			_ = s.ExecuteSync()
		}()
	}
	return ""
}

// DeleteLocalCache deletes the local blob copy of a VFS file.
func DeleteLocalCache(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_purge", map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.Storage.DeleteLocalCache(name)
	if err != nil {
		return err.Error()
	}
	return ""
}

// DeleteFile marks a VFS file as deleted in the registry.
func DeleteFile(name string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		_, err := sendUnixSocketCommand(appStorage, "vfs_delete", map[string]string{
			"name": name,
		})
		if err != nil {
			return err.Error()
		}
		return ""
	}

	err := s.Storage.DeleteLocalFile(name)
	if err != nil {
		return err.Error()
	}
	return ""
}

// GetLocalBlobPath returns absolute local file path for open operations.
func GetLocalBlobPath(hash string) string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		return filepath.Join(appStorage, "blobs", hash)
	}
	return s.Storage.GetLocalBlobPath(hash)
}
