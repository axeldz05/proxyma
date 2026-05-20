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
