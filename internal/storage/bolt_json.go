package storage

import (
	"encoding/json"
	"fmt"

	"go.etcd.io/bbolt"
)

// boltBucket opens a bucket inside tx, converting a missing bucket into an
// error. It is the shared guard every bolt helper and storage path builds on.
func boltBucket(tx *bbolt.Tx, bucket string) (*bbolt.Bucket, error) {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return nil, fmt.Errorf("%s bucket not found", bucket)
	}
	return b, nil
}

func boltPutJSON(tx *bbolt.Tx, bucket, key string, v any) error {
	b, err := boltBucket(tx, bucket)
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func boltGetJSON[T any](tx *bbolt.Tx, bucket, key string) (T, bool, error) {
	var zero T
	b, err := boltBucket(tx, bucket)
	if err != nil {
		return zero, false, err
	}
	data := b.Get([]byte(key))
	if data == nil {
		return zero, false, nil
	}
	var item T
	if err := json.Unmarshal(data, &item); err != nil {
		return zero, false, fmt.Errorf("corrupt JSON in %s/%s: %w", bucket, key, err)
	}
	return item, true, nil
}

func boltViewGetJSON[T any](db *bbolt.DB, bucket, key string) (T, bool, error) {
	var out T
	var exists bool
	err := db.View(func(tx *bbolt.Tx) error {
		var err error
		out, exists, err = boltGetJSON[T](tx, bucket, key)
		return err
	})
	return out, exists, err
}

func boltDelete(tx *bbolt.Tx, bucket, key string) error {
	b, err := boltBucket(tx, bucket)
	if err != nil {
		return err
	}
	return b.Delete([]byte(key))
}

func boltPutFlag(tx *bbolt.Tx, bucket, key string) error {
	b, err := boltBucket(tx, bucket)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), []byte("true"))
}

func boltHasKey(tx *bbolt.Tx, bucket, key string) (bool, error) {
	b, err := boltBucket(tx, bucket)
	if err != nil {
		return false, err
	}
	return b.Get([]byte(key)) != nil, nil
}

func boltLoadMapJSON[T any](db *bbolt.DB, bucket string) (map[string]T, error) {
	out := make(map[string]T)
	err := db.View(func(tx *bbolt.Tx) error {
		b, err := boltBucket(tx, bucket)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, v []byte) error {
			var item T
			if err := json.Unmarshal(v, &item); err != nil {
				return fmt.Errorf("corrupt JSON in %s/%s: %w", bucket, string(k), err)
			}
			out[string(k)] = item
			return nil
		})
	})
	return out, err
}
