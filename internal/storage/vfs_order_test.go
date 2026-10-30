package storage

import (
	"path/filepath"
	"testing"
	"time"

	"proxyma/internal/protocol"

	"go.etcd.io/bbolt"
)

func TestCompareIndexEntriesTotalOrder(t *testing.T) {
	base := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "aaaa", Size: 10}

	higherVersion := protocol.IndexEntry{Name: "doc.txt", Version: 4, Hash: "aaaa", Size: 10}
	tombstone := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "aaaa", Size: 10, Deleted: true}
	biggerHash := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "bbbb", Size: 10}
	biggerSize := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "aaaa", Size: 20}
	biggerName := protocol.IndexEntry{Name: "doc2.txt", Version: 3, Hash: "aaaa", Size: 10}

	cases := []struct {
		name      string
		candidate protocol.IndexEntry
		current   protocol.IndexEntry
		want      int
	}{
		{"higher version wins", higherVersion, base, 1},
		{"lower version loses", base, higherVersion, -1},
		{"tombstone beats live at equal version", tombstone, base, 1},
		{"live loses to tombstone at equal version", base, tombstone, -1},
		{"bigger hash wins at equal version", biggerHash, base, 1},
		{"smaller hash loses at equal version", base, biggerHash, -1},
		{"bigger size wins at equal version and hash", biggerSize, base, 1},
		{"smaller size loses at equal version and hash", base, biggerSize, -1},
		{"bigger name wins on full tie", biggerName, base, 1},
		{"smaller name loses on full tie", base, biggerName, -1},
		{"identical entries compare equal", base, base, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareIndexEntries(tc.candidate, tc.current); got != tc.want {
				t.Fatalf("compare(candidate, current) = %d, want %d", got, tc.want)
			}
			antisymmetry := -tc.want
			if got := compareIndexEntries(tc.current, tc.candidate); got != antisymmetry {
				t.Fatalf("swapped operands = %d, want %d (antisymmetry)", got, antisymmetry)
			}
		})
	}

	chain := []protocol.IndexEntry{base, biggerSize, biggerHash, tombstone, higherVersion}
	for i := 1; i < len(chain); i++ {
		if compareIndexEntries(chain[i], chain[i-1]) <= 0 {
			t.Fatalf("chain not strictly increasing at index %d", i)
		}
	}
}

func newTestVFS(t *testing.T) *VFS {
	t.Helper()
	db, err := bbolt.Open(filepath.Join(t.TempDir(), "vfs.db"), 0600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open vfs db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, name := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create buckets: %v", err)
	}
	return NewVFS(db)
}

func TestVFSUpsertFollowsTotalOrder(t *testing.T) {
	vfs := newTestVFS(t)
	live := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "aaaa", Size: 10}

	if updated, err := vfs.Upsert(live); err != nil || !updated {
		t.Fatalf("first upsert: updated=%v err=%v", updated, err)
	}

	older, err := vfs.Upsert(protocol.IndexEntry{Name: "doc.txt", Version: 2, Hash: "aaaa", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if older {
		t.Fatal("older version must not replace the current entry")
	}
	current, exists, err := vfs.Get("doc.txt")
	if err != nil || !exists || current.Version != 3 || current.Hash != "aaaa" {
		t.Fatalf("current entry should remain version 3 after older rejected: %+v", current)
	}

	equalLive, err := vfs.Upsert(live)
	if err != nil {
		t.Fatal(err)
	}
	if equalLive {
		t.Fatal("equal live entry must be a no-op")
	}

	tombstone := protocol.IndexEntry{Name: "doc.txt", Version: 3, Hash: "aaaa", Size: 10, Deleted: true}
	if _, err := vfs.Upsert(tombstone); err != nil {
		t.Fatal(err)
	}
	got, exists, err := vfs.Get("doc.txt")
	if err != nil || !exists {
		t.Fatalf("get after tombstone: exists=%v err=%v", exists, err)
	}
	if !got.Deleted {
		t.Fatal("equal-version tombstone must win over live entry")
	}
	if _, err := vfs.Upsert(live); err != nil {
		t.Fatal(err)
	}
	resurrected, _, err := vfs.Get("doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !resurrected.Deleted {
		t.Fatal("live entry must not resurrect an equal-version tombstone")
	}
}
