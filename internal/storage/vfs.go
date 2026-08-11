package storage

import (
	"proxyma/internal/protocol"

	"github.com/boltdb/bolt"
)

// IndexStore defines the abstraction for VFS metadata indexing.
// Implementing this interface allows plugging alternative storage backends (e.g. BadgerDB, SQLite, Pebble).
type IndexStore interface {
	Get(name string) (protocol.IndexEntry, bool)
	// Upsert reports whether the entry replaced an older version. A storage
	// failure must surface as an error, never as updated=false.
	Upsert(entry protocol.IndexEntry) (bool, error)
	Snapshot() map[string]protocol.IndexEntry
}

type VFS struct {
	index *bolt.DB
}

var _ IndexStore = (*VFS)(nil)

func NewVFS(index *bolt.DB) *VFS {
	return &VFS{
		index: index,
	}
}

func (v *VFS) Get(name string) (protocol.IndexEntry, bool) {
	var entry protocol.IndexEntry
	exists := false
	_ = v.index.View(func(tx *bolt.Tx) error {
		entry, exists = boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, name)
		return nil
	})
	return entry, exists
}

func (v *VFS) Upsert(entry protocol.IndexEntry) (bool, error) {
	updated := false
	err := v.index.Update(func(tx *bolt.Tx) error {
		if existing, ok := boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, entry.Name); ok {
			if existing.Version >= entry.Version {
				return nil
			}
		}
		if err := boltPutJSON(tx, vfsIndexBucket, entry.Name, entry); err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

func (v *VFS) Snapshot() map[string]protocol.IndexEntry {
	snapshot, err := boltLoadMapJSON[protocol.IndexEntry](v.index, vfsIndexBucket)
	if err != nil || snapshot == nil {
		return make(map[string]protocol.IndexEntry)
	}
	return snapshot
}
