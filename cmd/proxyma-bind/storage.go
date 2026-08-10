package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"proxyma/internal/protocol"
)

func GetVFSFilesJson() string {
	return InvokeDomainAction("storage", "list", nil)
}

// SyncVFS triggers VFS synchronization.
func SyncVFS() string {
	return InvokeDomainAction("storage", "sync", nil)
}

// UploadFile uploads a local file to the node's VFS.
func UploadFile(name string, filePath string) string {
	return InvokeDomainAction("storage", "upload", map[string]string{
		"path": filePath,
		"name": name,
	})
}

// SetSubscription enables/disables subscription for a VFS file.
func SetSubscription(name string, subscribe bool) string {
	action := "subscribe"
	if !subscribe {
		action = "unsubscribe"
	}
	return InvokeDomainAction("storage", action, map[string]string{"name": name})
}

// DeleteLocalCache deletes the local blob copy of a VFS file.
func DeleteLocalCache(name string) string {
	return InvokeDomainAction("storage", "purge", map[string]string{"name": name})
}

// DeleteFile marks a VFS file as deleted in the registry.
func DeleteFile(name string) string {
	return InvokeDomainAction("storage", "delete", map[string]string{"name": name})
}

// GetLocalBlobPath returns absolute local file path for open operations.
func GetLocalBlobPath(hash string) string {
	if s := getSrv(); s != nil {
		return s.Storage.GetBlobPath(hash)
	}
	return filepath.Join(appStorage, hash)
}

// ResolveTaskResultPath extracts a local filesystem path from a run/stream JSON response (L2).
func ResolveTaskResultPath(runJSON string) string {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(runJSON), &envelope); err != nil {
		return ""
	}
	outputs, _ := envelope["outputs"].(map[string]any)
	if outputs == nil {
		if nested, ok := envelope["data"].(map[string]any); ok {
			outputs, _ = nested["outputs"].(map[string]any)
		}
	}
	if path := protocol.ResultLocalPath(outputs); path != "" {
		return path
	}
	hash, _, _ := protocol.OutputHashFromOutputs(outputs)
	if hash == "" {
		return ""
	}
	return GetLocalBlobPath(hash)
}

// ResolveLocalBlob fetches a VFS file on demand and returns its local blob path (L2).
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
	return InvokeDomainAction("storage", "open", map[string]string{"name": name})
}
