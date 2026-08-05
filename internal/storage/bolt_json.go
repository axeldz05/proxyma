package storage

import (
	"encoding/json"
	"fmt"

	"github.com/boltdb/bolt"
)

func boltPutJSON(tx *bolt.Tx, bucket, key string, v any) error {
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

func boltGetJSON[T any](tx *bolt.Tx, bucket, key string) (T, bool) {
	var zero T
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return zero, false
	}
	data := b.Get([]byte(key))
	if data == nil {
		return zero, false
	}
	var item T
	if err := json.Unmarshal(data, &item); err != nil {
		return zero, false
	}
	return item, true
}

func boltDelete(tx *bolt.Tx, bucket, key string) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return fmt.Errorf("%s bucket not found", bucket)
	}
	return b.Delete([]byte(key))
}

func boltPutFlag(tx *bolt.Tx, bucket, key string) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return fmt.Errorf("%s bucket not found", bucket)
	}
	return b.Put([]byte(key), []byte("true"))
}

func boltHasKey(tx *bolt.Tx, bucket, key string) bool {
	b := tx.Bucket([]byte(bucket))
	return b != nil && b.Get([]byte(key)) != nil
}

func boltLoadMapJSON[T any](db *bolt.DB, bucket string) (map[string]T, error) {
	out := make(map[string]T)
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var item T
			if err := json.Unmarshal(v, &item); err == nil {
				out[string(k)] = item
			}
			return nil
		})
	})
	return out, err
}
