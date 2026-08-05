package utils

import (
	"encoding/json"
	"os"
)

// ReadJSONFile decodes a JSON file into dest (L1).
func ReadJSONFile(path string, dest any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewDecoder(f).Decode(dest)
}

// WriteJSONFile encodes v as indented JSON to path (L1).
func WriteJSONFile(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
