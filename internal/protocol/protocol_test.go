package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAddressRecordUnmarshaling(t *testing.T) {
	t.Parallel()

	jsonData := `{
		"addresses": ["192.168.1.10:8443", "203.0.113.5:8443"],
		"sequence": 12345
	}`

	var record protocol.AddressRecord
	err := json.Unmarshal([]byte(jsonData), &record)

	require.NoError(t, err)
	require.Len(t, record.Addresses, 2)
	require.Equal(t, "192.168.1.10:8443", record.Addresses[0])
	require.Equal(t, "203.0.113.5:8443", record.Addresses[1])
	require.Equal(t, int64(12345), record.Sequence)
}

func TestInferUIHint(t *testing.T) {
	t.Parallel()
	require.Equal(t, protocol.UIHintImagePicker, protocol.InferUIHint("image_path", "file"))
	require.Equal(t, protocol.UIHintImagePicker, protocol.InferUIHint("photo", "file"))
	require.Equal(t, protocol.UIHintImagePicker, protocol.InferUIHint("img_src", "file"))
	require.Equal(t, protocol.UIHintFilePicker, protocol.InferUIHint("document", "file"))
	require.Equal(t, "", protocol.InferUIHint("document", "string"))

	p := protocol.ServiceParameter{Type: "file", UIHint: protocol.UIHintAudioPicker}
	require.Equal(t, protocol.UIHintAudioPicker, protocol.EffectiveUIHint("img", p))
	require.Equal(t, protocol.UIHintFilePicker, protocol.EffectiveUIHint("doc", protocol.ServiceParameter{Type: "file"}))
	require.True(t, protocol.IsFilePickerHint(protocol.UIHintAudioPicker))
	require.True(t, protocol.IsImagePickerHint(protocol.UIHintImagePicker))
	require.False(t, protocol.IsImagePickerHint(protocol.UIHintFilePicker))
}

func TestNormalizeServiceSchema(t *testing.T) {
	t.Parallel()
	s := protocol.NormalizeServiceSchema("ocr", protocol.ServiceSchema{}, protocol.ServiceTypeScript)
	require.Equal(t, "ocr", s.Name)
	require.Equal(t, protocol.ServiceTypeScript, s.Type)

	s2 := protocol.NormalizeServiceSchema("x", protocol.ServiceSchema{
		Name: "keep",
		Type: protocol.ServiceTypeBidiStream,
	}, "")
	require.Equal(t, "keep", s2.Name)
	require.Equal(t, protocol.ServiceTypeGRPCBidi, s2.Type)
}

func TestDescribeParameter(t *testing.T) {
	t.Parallel()
	desc, hint := protocol.DescribeParameter("image_path", protocol.ServiceParameter{Type: protocol.ParamTypeFile})
	require.Equal(t, "image_picker", hint)
	require.Contains(t, desc, "image")

	desc2, _ := protocol.DescribeParameter("mode", protocol.ServiceParameter{
		Type:    protocol.ParamTypeString,
		Options: []string{"a", "b"},
	})
	require.Contains(t, desc2, "Options:")
}

func TestCoerceDefault(t *testing.T) {
	t.Parallel()
	require.Equal(t, true, protocol.ServiceParameter{Type: protocol.ParamTypeBool, Default: "true"}.CoerceDefault("x"))
	require.Equal(t, 42, protocol.ServiceParameter{Type: protocol.ParamTypeInt, Default: "42"}.CoerceDefault("x"))
	require.Equal(t, protocol.DefaultTCPPort, "8080")
}

func TestServiceTypeIsStreaming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		typ       protocol.ServiceType
		streaming bool
	}{
		{protocol.ServiceTypeExec, false},
		{protocol.ServiceTypeScript, false},
		{protocol.ServiceTypeGRPC, false},
		{protocol.ServiceTypeGRPCBidi, true},
		{protocol.ServiceTypeBidi, true},
		{protocol.ServiceTypeBidiStream, true}, // normalizes to grpc_bidi
		{protocol.ServiceTypeBidiGRPC, true},
		{protocol.ServiceTypeServerStream, true},
		{protocol.ServiceTypeHTTPServerStream, true},
		{protocol.ServiceTypeGRPCServerStream, true},
		{protocol.ServiceTypeHTTPBidi, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.typ), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.streaming, tc.typ.IsStreaming())
		})
	}
	require.Equal(t, protocol.ServiceTypeGRPCBidi, protocol.ServiceTypeBidiStream.Normalize())
	require.Equal(t, protocol.ServiceTypeBidi, protocol.ServiceTypeBidi.Normalize())
	require.Equal(t, protocol.ServiceTypeServerStream, protocol.ServiceTypeHTTPServerStream.Normalize())
	require.Equal(t, protocol.ServiceTypeServerStream, protocol.ServiceTypeGRPCServerStream.Normalize())
	require.Equal(t, protocol.ServiceTypeGRPCBidi, protocol.ServiceTypeHTTPBidi.Normalize())
}

func TestNormalizeSortStrategy(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", protocol.NormalizeSortStrategy(""))
	require.Equal(t, protocol.StrategyFastest, protocol.NormalizeSortStrategy("fastest"))
	require.Equal(t, protocol.StrategyCheapest, protocol.NormalizeSortStrategy("cheapest"))
	require.Equal(t, protocol.StrategyLowPower, protocol.NormalizeSortStrategy("low_power"))
	require.Equal(t, protocol.StrategyLowPower, protocol.NormalizeSortStrategy("low-power"))
	require.Equal(t, protocol.StrategyCheapest, protocol.NormalizeSortStrategy(protocol.StrategyCheapest))
}

func TestMatchServicePattern(t *testing.T) {
	t.Parallel()
	require.True(t, protocol.MatchServicePattern("ocr", "ocr"))
	require.False(t, protocol.MatchServicePattern("ocr", "translate"))
	require.True(t, protocol.MatchServicePattern("*", "anything"))
	require.True(t, protocol.MatchServicePattern("vision.*", "vision.ocr"))
	require.True(t, protocol.MatchServicePattern("vision.*", "vision"))
	require.False(t, protocol.MatchServicePattern("vision.*", "audio.ocr"))
	require.True(t, protocol.MatchServicePattern("vision*", "vision.ocr"))
}

func TestMissingRequired(t *testing.T) {
	t.Parallel()
	schema := protocol.ServiceSchema{
		Parameters: map[string]protocol.ServiceParameter{
			"file":   {Type: protocol.ParamTypeFile, Required: true},
			"mode":   {Type: protocol.ParamTypeString, Required: true},
			"optional": {Type: protocol.ParamTypeString, Required: false},
		},
	}
	require.Equal(t, []string{"file", "mode"}, protocol.MissingRequired(schema, nil))
	require.Equal(t, []string{"file", "mode"}, protocol.MissingRequired(schema, map[string]any{}))
	require.Equal(t, []string{"mode"}, protocol.MissingRequired(schema, map[string]any{"file": "/tmp/a"}))
	require.Equal(t, []string{"file"}, protocol.MissingRequired(schema, map[string]any{"file": "", "mode": "fast"}))
	require.Empty(t, protocol.MissingRequired(schema, map[string]any{"file": "/tmp/a", "mode": "fast"}))
	require.Equal(t, []string{"file"}, protocol.MissingRequired(schema, map[string]any{"file": nil, "mode": "x"}))
}

func TestVFSURIHelpers(t *testing.T) {
	t.Parallel()
	uri := protocol.VFSURI("abc123")
	require.Equal(t, "vfs://abc123", uri)
	require.True(t, protocol.IsVFSURI(uri))
	require.False(t, protocol.IsVFSURI("/tmp/file"))
	require.False(t, protocol.IsVFSURI(""))

	hash, ok := protocol.ParseVFSURI(uri)
	require.True(t, ok)
	require.Equal(t, "abc123", hash)

	_, ok = protocol.ParseVFSURI("/tmp/file")
	require.False(t, ok)
	_, ok = protocol.ParseVFSURI("vfs://")
	require.False(t, ok)
}

func TestIsStageableLocalPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		v    any
		ok   bool
		path string
	}{
		{"absolute", "/tmp/foo.pdf", true, "/tmp/foo.pdf"},
		{"relative", "foo.pdf", true, "foo.pdf"},
		{"vfs", "vfs://deadbeef", false, ""},
		{"empty", "", false, ""},
		{"nil", nil, false, ""},
		{"int", 42, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path, ok := protocol.IsStageableLocalPath(tc.v)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.path, path)
		})
	}
}

func TestRewriteLocalFilePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "doc.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))
	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(subdir, 0o755))

	staged := make(map[string]bool)
	stage := func(path string) (string, int64, error) {
		staged[path] = true
		return "hash99", 5, nil
	}

	m := map[string]any{
		"input":    filePath,
		"already":  "vfs://existing",
		"missing":  filepath.Join(dir, "nope.txt"),
		"dir":      subdir,
		"label":    "not-a-path-value",
		"nil_stage": nil,
	}
	protocol.RewriteLocalFilePaths(m, stage, false)
	require.Equal(t, "vfs://hash99", m["input"])
	require.Equal(t, "vfs://existing", m["already"])
	require.Equal(t, filepath.Join(dir, "nope.txt"), m["missing"]) // stage skipped: file missing
	require.Equal(t, subdir, m["dir"])
	require.True(t, staged[filePath])
	require.False(t, staged[subdir])

	// annotateOutputs writes output metadata keys
	out := map[string]any{"result": filePath}
	protocol.RewriteLocalFilePaths(out, stage, true)
	require.Equal(t, "vfs://hash99", out["result"])
	require.Equal(t, "hash99", out[protocol.OutputHashKey])
	require.Equal(t, "doc.txt", out[protocol.OutputNameKey])
	require.Equal(t, float64(5), out[protocol.OutputSizeKey])

	// nil map / nil stage are no-ops
	protocol.RewriteLocalFilePaths(nil, stage, false)
	protocol.RewriteLocalFilePaths(m, nil, false)
}

func TestOutputHashAndResultLocalPath(t *testing.T) {
	t.Parallel()
	hash, name, size := protocol.OutputHashFromOutputs(map[string]any{
		protocol.OutputHashKey: "h1",
		protocol.OutputNameKey: "out.pdf",
		protocol.OutputSizeKey: float64(100),
	})
	require.Equal(t, "h1", hash)
	require.Equal(t, "out.pdf", name)
	require.Equal(t, int64(100), size)

	hash, name, size = protocol.OutputHashFromOutputs(map[string]any{
		"file": "vfs://deadbeef",
	})
	require.Equal(t, "deadbeef", hash)
	require.Equal(t, "file", name)
	require.Equal(t, int64(0), size)

	require.Equal(t, "", protocol.ResultLocalPath(nil))
	require.Equal(t, "/tmp/out.pdf", protocol.ResultLocalPath(map[string]any{
		protocol.ResultLocalPathKey: "/tmp/out.pdf",
	}))
	require.Equal(t, "/tmp/alt.pdf", protocol.ResultLocalPath(map[string]any{
		"output_path": "/tmp/alt.pdf",
	}))
	require.Equal(t, "", protocol.ResultLocalPath(map[string]any{
		"output_path": "vfs://onlyhash",
	}))
}

func TestPathConstantsAndPathRel(t *testing.T) {
	t.Parallel()
	require.True(t, strings.HasPrefix(protocol.PathUpload, "/"))
	require.True(t, strings.HasPrefix(protocol.PathClusterJoin, "/"))
	require.True(t, strings.HasPrefix(protocol.PathServicesBid, "/"))
	require.True(t, strings.HasPrefix(protocol.ServicesPrefix, "/"))
	require.Equal(t, "upload", protocol.PathRel(protocol.PathUpload))
	require.Equal(t, "cluster/join", protocol.PathRel(protocol.PathClusterJoin))
	require.Equal(t, "peers", protocol.PathRel("peers")) // already relative
	require.Equal(t, 65536, protocol.MaxRelayBodyBytes)
}

func TestRPCTimeouts(t *testing.T) {
	t.Parallel()
	require.Equal(t, 10*time.Second, protocol.RPCTimeoutSync)
	require.Equal(t, 90*time.Second, protocol.RPCTimeoutTaskWait)
	require.Equal(t, 5*time.Second, protocol.RPCTimeoutTaskCallback)
	require.Less(t, protocol.RPCTimeoutTaskCallback, protocol.RPCTimeoutSync)
	require.Less(t, protocol.RPCTimeoutSync, protocol.RPCTimeoutTaskWait)
}
