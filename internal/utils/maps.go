package utils

import (
	"maps"
)

// CloneJSONMap recursively copies JSON-shaped maps and slices.
func CloneJSONMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneJSONValue(value)
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneJSONMap(typed)
	case map[string]map[string]any:
		cloned := make(map[string]map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = CloneJSONMap(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJSONValue(item)
		}
		return cloned
	default:
		return typed
	}
}

// CloneMap returns a copy of m, optionally applying cloneValue to each element.
// If cloneValue is nil, it delegates to maps.Clone for a standard shallow copy.
func CloneMap[K comparable, V any](m map[K]V, cloneValue func(V) V) map[K]V {
	if m == nil {
		return nil
	}
	if cloneValue == nil {
		return maps.Clone(m)
	}
	cloned := make(map[K]V, len(m))
	for k, v := range m {
		cloned[k] = cloneValue(v)
	}
	return cloned
}

// PopulateMapIf inserts elements from keys into dst with val if predicate is nil or evaluates to true.
func PopulateMapIf[K comparable, V any](dst map[K]V, keys []K, val V, predicate func(K) bool) {
	if dst == nil {
		return
	}
	for _, k := range keys {
		if predicate == nil || predicate(k) {
			dst[k] = val
		}
	}
}

// PopulateMap unconditionally inserts elements from keys into dst with val.
func PopulateMap[K comparable, V any](dst map[K]V, keys []K, val V) {
	PopulateMapIf(dst, keys, val, nil)
}

// CopyMapIf copies entries from src to dst for which predicate evaluates to true.
// If predicate is nil, all entries are copied unconditionally.
func CopyMapIf[K comparable, V any](dst, src map[K]V, predicate func(k K, v V) bool) {
	if dst == nil || src == nil {
		return
	}
	for k, v := range src {
		if predicate == nil || predicate(k, v) {
			dst[k] = v
		}
	}
}

// MergeMap copies entries from src to dst.
// If overwrite is true, existing keys in dst are overwritten;
// if overwrite is false, existing keys in dst are preserved.
func MergeMap[K comparable, V any](dst, src map[K]V, overwrite bool) {
	if overwrite {
		CopyMapIf(dst, src, nil)
	} else {
		CopyMapIf(dst, src, func(k K, _ V) bool {
			_, exists := dst[k]
			return !exists
		})
	}
}
