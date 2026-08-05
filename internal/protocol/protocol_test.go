package protocol_test

import (
	"encoding/json"
	"proxyma/internal/protocol"
	"testing"

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
	require.Equal(t, "image_picker", protocol.InferUIHint("image_path", "file"))
	require.Equal(t, "image_picker", protocol.InferUIHint("photo", "file"))
	require.Equal(t, "image_picker", protocol.InferUIHint("img_src", "file"))
	require.Equal(t, "file_picker", protocol.InferUIHint("document", "file"))
	require.Equal(t, "", protocol.InferUIHint("document", "string"))

	p := protocol.ServiceParameter{Type: "file", UIHint: "audio_picker"}
	require.Equal(t, "audio_picker", protocol.EffectiveUIHint("img", p))
	require.Equal(t, "file_picker", protocol.EffectiveUIHint("doc", protocol.ServiceParameter{Type: "file"}))
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
