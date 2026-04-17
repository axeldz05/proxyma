package storage

import (
	"encoding/json"
	"github.com/boltdb/bolt"
	"proxyma/internal/protocol"
)

type VFS struct {
	index *bolt.DB
}

func NewVFS(index *bolt.DB) *VFS {
	return &VFS{
		index: index,
	}
}

func (v *VFS) Get(name string) (protocol.IndexEntry, bool) {
	var entry protocol.IndexEntry
	exists := false

	_ = v.index.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("vfs_index"))
		if b == nil {
			return nil
		}
		data := b.Get([]byte(name))
		if data != nil {
			if err := json.Unmarshal(data, &entry); err == nil {
				exists = true
			}
		}
		return nil
	})
	return entry, exists
}

func (v *VFS) Upsert(entry protocol.IndexEntry) bool {
	updated := false
	_ = v.index.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("vfs_index"))

		data := b.Get([]byte(entry.Name))
		if data != nil {
			var existing protocol.IndexEntry
			if err := json.Unmarshal(data, &existing); err == nil {
				if existing.Version >= entry.Version {
					return nil
				}
			}
		}
		newData, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(entry.Name), newData); err == nil {
			updated = true
		}
		return nil
	})
	return updated
}

func (v *VFS) Snapshot() map[string]protocol.IndexEntry {
	snapshot := make(map[string]protocol.IndexEntry)

	_ = v.index.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("vfs_index"))
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			var entry protocol.IndexEntry
			if err := json.Unmarshal(v, &entry); err == nil {
				snapshot[string(k)] = entry
			}
			return nil
		})
	})
	return snapshot
}
