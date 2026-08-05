package utils

import (
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
)

// RewriteLocalFilePaths stages local file path values in m via stage and rewrites them to vfs:// URIs (L2).
// When annotateOutputs is true, also sets output_hash / output_name / output_size.
func RewriteLocalFilePaths(m map[string]any, stage func(path string) (hash string, size int64, err error), annotateOutputs bool) {
	if m == nil || stage == nil {
		return
	}
	for k, v := range m {
		pathStr, ok := protocol.IsStageableLocalPath(v)
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
			m["output_hash"] = hash
			m["output_name"] = filepath.Base(pathStr)
			m["output_size"] = float64(size)
		}
		m[k] = protocol.VFSURI(hash)
	}
}
