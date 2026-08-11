package storage

import (
	"encoding/json"
	"fmt"

	"go.etcd.io/bbolt"
)

func boltPutJSON(tx *bbolt.Tx, bucket, key string, v any) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return fmt.Errorf("%s bucket not found", bucket)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func boltGetJSON[T any](tx *bbolt.Tx, bucket, key string) (T, bool, error) {
	var zero T
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return zero, false, nil
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

func boltDelete(tx *bbolt.Tx, bucket, key string) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return fmt.Errorf("%s bucket not found", bucket)
	}
	return b.Delete([]byte(key))
}

func boltPutFlag(tx *bbolt.Tx, bucket, key string) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return fmt.Errorf("%s bucket not found", bucket)
	}
	return b.Put([]byte(key), []byte("true"))
}

func boltHasKey(tx *bbolt.Tx, bucket, key string) bool {
	b := tx.Bucket([]byte(bucket))
	return b != nil && b.Get([]byte(key)) != nil
}

func boltLoadMapJSON[T any](db *bbolt.DB, bucket string) (map[string]T, error) {
	out := make(map[string]T)
	err := db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
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
