package server_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func subscribeVFSFileViaHTTP(t *testing.T, node *TestServer, name string) {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodPost,
		node.Config.Address+protocol.PathSubscribe+"?name="+url.QueryEscape(name),
		nil,
	)
	require.NoError(t, err)
	resp, err := node.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func readVFSBlobViaHTTP(node *TestServer, hash string) ([]byte, int, error) {
	req, err := http.NewRequest(
		http.MethodGet,
		node.Config.Address+protocol.PathDownloadPrefix+hash,
		nil,
	)
	if err != nil {
		return nil, 0, err
	}
	resp, err := node.Client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

func waitForPublicVFSFile(
	t *testing.T,
	node *TestServer,
	name string,
	matches func(protocol.IndexEntry) bool,
) protocol.IndexEntry {
	t.Helper()
	var got protocol.IndexEntry
	require.Eventually(t, func() bool {
		entry, ok := RequestManifestSimulated(t, node)[name]
		if !ok || !matches(entry) {
			return false
		}
		got = entry
		return true
	}, 5*time.Second, 50*time.Millisecond)
	return got
}

func assertPublicVFSContent(t *testing.T, node *TestServer, hash, content string) {
	t.Helper()
	require.Eventually(t, func() bool {
		body, status, err := readVFSBlobViaHTTP(node, hash)
		return err == nil && status == http.StatusOK && string(body) == content
	}, 5*time.Second, 50*time.Millisecond)
}

func TestVFSReuploadSameNameConvergesAcrossCluster(t *testing.T) {
	t.Parallel()

	const (
		name      = "versioned-document.txt"
		contentV1 = "version one"
		contentV2 = "version two replaces version one"
	)
	hashV2 := testutil.CalculateHash(t, contentV2)

	author := NewServer(t, testutil.DefaultConfig(t, "version-author"), nil)
	mirrorA := NewServer(t, testutil.DefaultConfig(t, "version-mirror-a"), nil)
	mirrorB := NewServer(t, testutil.DefaultConfig(t, "version-mirror-b"), nil)
	linkClusterPeers(t, author, mirrorA, mirrorB)
	subscribeVFSFileViaHTTP(t, mirrorA, name)
	subscribeVFSFileViaHTTP(t, mirrorB, name)

	UploadFileSimulated(t, author, name, contentV1)
	require.NoError(t, mirrorA.ExecuteSync())
	require.NoError(t, mirrorB.ExecuteSync())
	waitForPublicVFSFile(t, mirrorA, name, func(entry protocol.IndexEntry) bool {
		return entry.Version == 1 && !entry.Deleted
	})

	UploadFileSimulated(t, author, name, contentV2)
	require.NoError(t, mirrorA.ExecuteSync())
	require.NoError(t, mirrorB.ExecuteSync())

	for _, node := range []*TestServer{author, mirrorA, mirrorB} {
		entry := waitForPublicVFSFile(t, node, name, func(entry protocol.IndexEntry) bool {
			return entry.Version == 2 && entry.Hash == hashV2 && !entry.Deleted
		})
		assertPublicVFSContent(t, node, entry.Hash, contentV2)
	}
}

func TestVFSEqualVersionConflictConvergesAfterPartitionHeal(t *testing.T) {
	t.Parallel()

	const (
		name     = "partition-conflict.txt"
		contentA = "content written while partition A is isolated"
		contentB = "different content written while partition B is isolated"
	)
	hashA := testutil.CalculateHash(t, contentA)
	hashB := testutil.CalculateHash(t, contentB)
	require.NotEqual(t, hashA, hashB)
	winnerHash, winnerContent := hashA, contentA
	if hashB > hashA {
		winnerHash, winnerContent = hashB, contentB
	}

	nodeA := NewServer(t, testutil.DefaultConfig(t, "partition-writer-a"), nil)
	nodeB := NewServer(t, testutil.DefaultConfig(t, "partition-writer-b"), nil)
	subscribeVFSFileViaHTTP(t, nodeA, name)
	subscribeVFSFileViaHTTP(t, nodeB, name)

	UploadFileSimulated(t, nodeA, name, contentA)
	UploadFileSimulated(t, nodeB, name, contentB)
	require.Equal(t, 1, RequestManifestSimulated(t, nodeA)[name].Version)
	require.Equal(t, 1, RequestManifestSimulated(t, nodeB)[name].Version)

	linkClusterPeers(t, nodeA, nodeB)
	require.NoError(t, nodeA.ExecuteSync())
	require.NoError(t, nodeB.ExecuteSync())

	for _, node := range []*TestServer{nodeA, nodeB} {
		entry := waitForPublicVFSFile(t, node, name, func(entry protocol.IndexEntry) bool {
			return entry.Version == 1 && entry.Hash == winnerHash && !entry.Deleted
		})
		assertPublicVFSContent(t, node, entry.Hash, winnerContent)
	}
	require.Equal(
		t,
		RequestManifestSimulated(t, nodeA)[name],
		RequestManifestSimulated(t, nodeB)[name],
	)
}

func TestVFSCorruptReplicaFallsBackToValidPeer(t *testing.T) {
	t.Parallel()

	const (
		name    = "replicated-integrity.txt"
		content = "the canonical replicated payload"
	)
	hash := testutil.CalculateHash(t, content)

	consumerConfig := testutil.DefaultConfig(t, "integrity-consumer")
	consumerConfig.Workers = 1
	consumer := NewServer(t, consumerConfig, nil)
	corrupt := NewServer(t, testutil.DefaultConfig(t, "integrity-corrupt"), nil)
	valid := NewServer(t, testutil.DefaultConfig(t, "integrity-valid"), nil)

	UploadFileSimulated(t, corrupt, name, content)
	UploadFileSimulated(t, valid, name, content)
	require.NoError(t, os.WriteFile(corrupt.Storage.GetBlobPath(hash), []byte("tampered"), 0o600))

	linkClusterPeers(t, consumer, corrupt, valid)
	subscribeVFSFileViaHTTP(t, consumer, name)
	entry := RequestManifestSimulated(t, corrupt)[name]
	require.Equal(t, hash, entry.Hash)

	err := corrupt.PeerClient().Notify(context.Background(), consumer.Config.ID, protocol.PeerNotification{
		File:   entry,
		Source: corrupt.Config.Address,
	})
	require.NoError(t, err)

	waitForPublicVFSFile(t, consumer, name, func(got protocol.IndexEntry) bool {
		return got == entry
	})
	assertPublicVFSContent(t, consumer, hash, content)
}

func TestVFSFetchFileOnDemandSucceedsFromRemotePeer(t *testing.T) {
	t.Parallel()

	const (
		name    = "on-demand-remote.txt"
		content = "remote content fetched only when requested"
	)
	hash := testutil.CalculateHash(t, content)

	provider := NewServer(t, testutil.DefaultConfig(t, "on-demand-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "on-demand-consumer"), nil)
	UploadFileSimulated(t, provider, name, content)
	linkClusterPeers(t, provider, consumer)

	require.NoError(t, consumer.ExecuteSync())
	entry := waitForPublicVFSFile(t, consumer, name, func(entry protocol.IndexEntry) bool {
		return entry.Hash == hash && !entry.Deleted
	})
	body, status, err := readVFSBlobViaHTTP(consumer, entry.Hash)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.NotEqual(t, content, string(body))

	require.NoError(t, consumer.FetchFileOnDemand(name))
	assertPublicVFSContent(t, consumer, hash, content)
}
