package storage

import (
	"proxyma/internal/protocol"

	"go.etcd.io/bbolt"
)

// IndexStore defines the abstraction for VFS metadata indexing.
// Implementing this interface allows plugging alternative storage backends (e.g. BadgerDB, SQLite, Pebble).
type IndexStore interface {
	Get(name string) (protocol.IndexEntry, bool)
	// Upsert reports whether the entry replaced an older version. A storage
	// failure must surface as an error, never as updated=false.
	Upsert(entry protocol.IndexEntry) (bool, error)
	// UpsertAutoVersion bumps Version atomically when Version<=0.
	UpsertAutoVersion(entry protocol.IndexEntry) (protocol.IndexEntry, error)
	Snapshot() map[string]protocol.IndexEntry
}

type VFS struct {
	index *bbolt.DB
}

var _ IndexStore = (*VFS)(nil)

func NewVFS(index *bbolt.DB) *VFS {
	return &VFS{
		index: index,
	}
}

func (v *VFS) Get(name string) (protocol.IndexEntry, bool) {
	var entry protocol.IndexEntry
	exists := false
	_ = v.index.View(func(tx *bbolt.Tx) error {
		var err error
		entry, exists, err = boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, name)
		return err
	})
	return entry, exists
}

func (v *VFS) Upsert(entry protocol.IndexEntry) (bool, error) {
	updated := false
	err := v.index.Update(func(tx *bbolt.Tx) error {
		if existing, ok, err := boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, entry.Name); err != nil {
			return err
		} else if ok {
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

// UpsertAutoVersion bumps Version atomically inside the Bolt transaction when Version<=0 (M5).
func (v *VFS) UpsertAutoVersion(entry protocol.IndexEntry) (protocol.IndexEntry, error) {
	err := v.index.Update(func(tx *bbolt.Tx) error {
		if entry.Version <= 0 {
			entry.Version = 1
			if existing, ok, err := boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, entry.Name); err != nil {
				return err
			} else if ok {
				entry.Version = existing.Version + 1
			}
		} else if existing, ok, err := boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, entry.Name); err != nil {
			return err
		} else if ok && existing.Version >= entry.Version {
			return nil
		}
		return boltPutJSON(tx, vfsIndexBucket, entry.Name, entry)
	})
	return entry, err
}

func (v *VFS) Snapshot() map[string]protocol.IndexEntry {
	snapshot, err := boltLoadMapJSON[protocol.IndexEntry](v.index, vfsIndexBucket)
	if err != nil || snapshot == nil {
		return make(map[string]protocol.IndexEntry)
	}
	return snapshot
}
