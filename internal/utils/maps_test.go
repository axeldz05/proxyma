package utils_test

import (
	"proxyma/internal/utils"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneJSONMap(t *testing.T) {
	t.Parallel()

	require.Nil(t, utils.CloneJSONMap(nil))

	orig := map[string]any{
		"str":  "hello",
		"num":  42.0,
		"bool": true,
		"list": []any{"a", map[string]any{"nested": "value"}},
		"obj": map[string]any{
			"deep": "field",
		},
		"strMap": map[string]string{
			"k1": "v1",
		},
	}

	cloned := utils.CloneJSONMap(orig)
	require.Equal(t, orig, cloned)

	// Verify deep isolation
	cloned["str"] = "world"
	require.Equal(t, "hello", orig["str"])

	clonedList := cloned["list"].([]any)
	clonedList[0] = "mutated"
	require.Equal(t, "a", orig["list"].([]any)[0])

	clonedObj := cloned["obj"].(map[string]any)
	clonedObj["deep"] = "mutated"
	require.Equal(t, "field", orig["obj"].(map[string]any)["deep"])
}

func TestCloneMap(t *testing.T) {
	t.Parallel()

	require.Nil(t, utils.CloneMap[string, int](nil, nil))

	src := map[string]int{"a": 1, "b": 2}
	// Shallow copy with nil cloneValue
	cloned := utils.CloneMap(src, nil)
	require.Equal(t, src, cloned)

	// Copy with value transformer
	doubled := utils.CloneMap(src, func(v int) int { return v * 2 })
	require.Equal(t, map[string]int{"a": 2, "b": 4}, doubled)
}

func TestPopulateMap(t *testing.T) {
	t.Parallel()

	t.Run("nil destination is safe", func(t *testing.T) {
		var m map[string]bool
		utils.PopulateMap(m, []string{"a", "b"}, true)
		require.Nil(t, m)

		utils.PopulateMapIf(m, []string{"a", "b"}, true, func(string) bool { return true })
		require.Nil(t, m)
	})

	t.Run("unconditional populate", func(t *testing.T) {
		m := make(map[string]int)
		utils.PopulateMap(m, []string{"x", "y"}, 10)
		require.Equal(t, map[string]int{"x": 10, "y": 10}, m)
	})

	t.Run("conditional populate", func(t *testing.T) {
		m := make(map[string]bool)
		keys := []string{"keep-1", "drop-1", "keep-2"}
		utils.PopulateMapIf(m, keys, true, func(k string) bool {
			return len(k) > 4 && k[:4] == "keep"
		})
		require.Equal(t, map[string]bool{"keep-1": true, "keep-2": true}, m)
	})
}

func TestCopyMapIf(t *testing.T) {
	t.Parallel()

	t.Run("nil safety", func(t *testing.T) {
		var nilDst map[string]int
		utils.CopyMapIf(nilDst, map[string]int{"a": 1}, nil)
		require.Nil(t, nilDst)

		dst := make(map[string]int)
		utils.CopyMapIf(dst, nil, nil)
		require.Empty(t, dst)
	})

	t.Run("unconditional copy", func(t *testing.T) {
		dst := map[string]int{"a": 1}
		src := map[string]int{"b": 2, "c": 3}
		utils.CopyMapIf(dst, src, nil)
		require.Equal(t, map[string]int{"a": 1, "b": 2, "c": 3}, dst)
	})

	t.Run("conditional copy with predicate", func(t *testing.T) {
		dst := make(map[string]int)
		src := map[string]int{"even": 2, "odd": 3, "another_even": 4}
		utils.CopyMapIf(dst, src, func(k string, v int) bool {
			return v%2 == 0
		})
		require.Equal(t, map[string]int{"even": 2, "another_even": 4}, dst)
	})
}

func TestMergeMap(t *testing.T) {
	t.Parallel()

	t.Run("overwrite true overrides existing keys", func(t *testing.T) {
		dst := map[string]string{"k1": "old", "k2": "keep"}
		src := map[string]string{"k1": "new", "k3": "added"}
		utils.MergeMap(dst, src, true)
		require.Equal(t, map[string]string{
			"k1": "new",
			"k2": "keep",
			"k3": "added",
		}, dst)
	})

	t.Run("overwrite false preserves existing keys", func(t *testing.T) {
		dst := map[string]string{"k1": "old", "k2": "keep"}
		src := map[string]string{"k1": "new", "k3": "added"}
		utils.MergeMap(dst, src, false)
		require.Equal(t, map[string]string{
			"k1": "old",
			"k2": "keep",
			"k3": "added",
		}, dst)
	})
}
