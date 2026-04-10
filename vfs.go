package main
import(
	"maps"
	"sync"
)

type VFS struct {
    index map[string]IndexEntry
    mu    sync.RWMutex
}

func NewVFS() *VFS {
	return &VFS {
		index: make(map[string]IndexEntry),
	}
}

func (v *VFS) Get(name string) (IndexEntry, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	entry, exists := v.index[name]
	return entry, exists
}

func (v *VFS) Upsert(entry IndexEntry) bool {
    v.mu.Lock()
	defer v.mu.Unlock()
	existingMeta, exists := v.index[entry.Name]
	if !exists || (exists && existingMeta.Version < entry.Version){
		v.index[entry.Name] = entry
		return true
    }
	return false
}

func (v *VFS) Snapshot() map[string]IndexEntry {
	v.mu.RLock()
	defer v.mu.RUnlock()
	snapshot := make(map[string]IndexEntry)

    maps.Copy(snapshot, v.index)
	return snapshot
}
