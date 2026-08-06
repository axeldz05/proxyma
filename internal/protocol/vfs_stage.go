package protocol

import (
	"os"
	"path/filepath"
)

// RewriteLocalFilePaths stages local file path values in m via stage and rewrites them to vfs:// URIs (L2).
// Recurses into nested map[string]any values. When annotateOutputs is true, also sets
// OutputHashKey / OutputNameKey / OutputSizeKey on the map that owns the staged path.
func RewriteLocalFilePaths(m map[string]any, stage func(path string) (hash string, size int64, err error), annotateOutputs bool) {
	if m == nil || stage == nil {
		return
	}
	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			RewriteLocalFilePaths(nested, stage, annotateOutputs)
			continue
		}
		pathStr, ok := IsStageableLocalPath(v)
		if !ok {
			continue
		}
		fi, err := os.Stat(pathStr)
		if err != nil || fi.IsDir() {
			continue
		}
		hash, size, err := stage(pathStr)
		if err != nil || hash == "" {
			continue
		}
		if annotateOutputs {
			m[OutputHashKey] = hash
			m[OutputNameKey] = filepath.Base(pathStr)
			m[OutputSizeKey] = float64(size)
		}
		m[k] = VFSURI(hash)
	}
}
