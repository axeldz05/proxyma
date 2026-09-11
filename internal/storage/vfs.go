package storage

import (
	"proxyma/internal/protocol"

	"go.etcd.io/bbolt"
)

// IndexStore defines the abstraction for VFS metadata indexing.
// Implementing this interface allows plugging alternative storage backends (e.g. BadgerDB, SQLite, Pebble).
type IndexStore interface {
	Get(name string) (protocol.IndexEntry, bool, error)
	// Upsert reports whether the entry replaced an older version. A storage
	// failure must surface as an error, never as updated=false.
	Upsert(entry protocol.IndexEntry) (bool, error)
	// UpsertAutoVersion bumps Version atomically when Version<=0.
	UpsertAutoVersion(entry protocol.IndexEntry) (protocol.IndexEntry, error)
	// Snapshot returns the full index. On load failure it must return an error —
	// never an empty map that would look like an empty VFS (orphan GC hazard).
	Snapshot() (map[string]protocol.IndexEntry, error)
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

func (v *VFS) Get(name string) (protocol.IndexEntry, bool, error) {
	var entry protocol.IndexEntry
	exists := false
	err := v.index.View(func(tx *bbolt.Tx) error {
		var err error
		entry, exists, err = boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, name)
		return err
	})
	return entry, exists, err
}

// compareIndexEntries defines the replicated VFS total order. Higher versions
// win. At equal version tombstones dominate live entries, then hash, size, and
// name compare lexicographically/numerically. Every peer therefore chooses the
// same winner regardless of arrival order, and equal-version deletes cannot be
// resurrected by a live sibling.
func compareIndexEntries(candidate, current protocol.IndexEntry) int {
	if candidate.Version != current.Version {
		if candidate.Version > current.Version {
			return 1
		}
		return -1
	}
	if candidate.Deleted != current.Deleted {
		if candidate.Deleted {
			return 1
		}
		return -1
	}
	if candidate.Hash != current.Hash {
		if candidate.Hash > current.Hash {
			return 1
		}
		return -1
	}
	if candidate.Size != current.Size {
		if candidate.Size > current.Size {
			return 1
		}
		return -1
	}
	if candidate.Name != current.Name {
		if candidate.Name > current.Name {
			return 1
		}
		return -1
	}
	return 0
}

func queueSupersededBlobGCTx(tx *bbolt.Tx, previous, next protocol.IndexEntry) error {
	if previous.Hash == "" || (!next.Deleted && previous.Hash == next.Hash) {
		return nil
	}
	return boltPutFlag(tx, bucketPendingBlobGC, previous.Hash)
}

// resolveVFSWrite is the shared write core of every VFS upsert variant. Inside
// an open tx it loads the current entry, applies the replicated total order
// (rejecting non-winning explicit versions and resolving Version<=0 auto-bumps),
// writes the winner and queues superseded-blob GC. It returns the effective
// entry (existing on a no-op, the written entry otherwise) plus the previous
// entry and whether it existed.
func resolveVFSWrite(tx *bbolt.Tx, entry protocol.IndexEntry) (written, existing protocol.IndexEntry, existed, applied bool, err error) {
	existing, existed, err = boltGetJSON[protocol.IndexEntry](tx, vfsIndexBucket, entry.Name)
	if err != nil {
		return existing, existing, existed, false, err
	}
	if entry.Version > 0 {
		if existed && compareIndexEntries(entry, existing) <= 0 {
			return existing, existing, existed, false, nil
		}
	} else {
		entry.Version = 1
		if existed {
			entry.Version = existing.Version + 1
		}
	}
	if err := boltPutJSON(tx, vfsIndexBucket, entry.Name, entry); err != nil {
		return existing, existing, existed, false, err
	}
	if existed {
		if err := queueSupersededBlobGCTx(tx, existing, entry); err != nil {
			return existing, existing, existed, false, err
		}
	}
	return entry, existing, existed, true, nil
}

func (v *VFS) Upsert(entry protocol.IndexEntry) (bool, error) {
	updated := false
	err := v.index.Update(func(tx *bbolt.Tx) error {
		_, _, _, applied, err := resolveVFSWrite(tx, entry)
		if err != nil {
			return err
		}
		updated = applied
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
		written, _, _, _, err := resolveVFSWrite(tx, entry)
		if err != nil {
			return err
		}
		entry = written
		return nil
	})
	return entry, err
}

type indexSubscriptionMutation struct {
	Entry       protocol.IndexEntry
	Previous    protocol.IndexEntry
	HadPrevious bool
	Applied     bool
}

// upsertAndSubscribe commits VFS metadata and its subscription flag in one
// bbolt transaction. Explicit non-winning versions are no-ops.
func (v *VFS) upsertAndSubscribe(entry protocol.IndexEntry) (indexSubscriptionMutation, error) {
	result := indexSubscriptionMutation{Entry: entry}
	applied := false
	err := v.index.Update(func(tx *bbolt.Tx) error {
		written, existing, existed, appliedTx, err := resolveVFSWrite(tx, entry)
		if err != nil {
			return err
		}
		result.Previous = existing
		result.HadPrevious = existed
		if !appliedTx {
			result.Entry = written // no-op: helper returns the existing entry
			return nil
		}
		if err := boltPutFlag(tx, bucketSubscriptions, entry.Name); err != nil {
			return err
		}
		result.Entry = written
		applied = true
		return nil
	})
	if err != nil {
		result.Applied = false
		return result, err
	}
	result.Applied = applied
	return result, nil
}

func (v *VFS) Snapshot() (map[string]protocol.IndexEntry, error) {
	snapshot, err := boltLoadMapJSON[protocol.IndexEntry](v.index, vfsIndexBucket)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return make(map[string]protocol.IndexEntry), nil
	}
	return snapshot, nil
}
