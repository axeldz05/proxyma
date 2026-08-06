package server_test

import (
	"bytes"
	"context"
	"io"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNamelessFetchRejectsHashMismatch(t *testing.T) {
	t.Parallel()

	expectedContent := "canonical-payload"
	expectedHash := testutil.CalculateHash(t, expectedContent)
	corruptContent := "tampered-payload"

	mockClient := &testutil.MockPeerClient{
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(corruptContent))), nil
		},
	}

	srv := NewServer(t, testutil.DefaultConfig(t, "nameless-fetch"), mockClient)
	srv.AddPeer("peer-a", protocol.AddressRecord{Addresses: []string{"https://fake:8080"}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := srv.FetchBlobFromPeer(ctx, "peer-a", protocol.IndexEntry{Hash: expectedHash})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hash mismatch")

	hasExpected, _ := srv.Storage.HasPhysicalBlob(expectedHash)
	require.False(t, hasExpected, "requested hash must not appear local after mismatch")

	corruptHash := testutil.CalculateHash(t, corruptContent)
	hasCorrupt, _ := srv.Storage.HasPhysicalBlob(corruptHash)
	require.False(t, hasCorrupt, "corrupted blob must not remain as usable OK")
}
