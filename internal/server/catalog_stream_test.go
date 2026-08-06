package server_test

import (
	"context"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCatalogSyncsServicesOnAddPeer(t *testing.T) {
	t.Parallel()

	provider := NewServer(t, testutil.DefaultConfig(t, "catalog-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "catalog-consumer"), nil)

	require.NoError(t, provider.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:        "shared-ocr",
		Description: "cataloged OCR",
		Parameters: map[string]protocol.ServiceParameter{
			"file": {Type: protocol.ParamTypeFile, Required: true},
		},
	}, compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	linkClusterPeers(t, provider, consumer)

	require.Eventually(t, func() bool {
		schema, ok := consumer.Peers.GetServiceSchema("shared-ocr")
		return ok && schema.Description == "cataloged OCR"
	}, 3*time.Second, 50*time.Millisecond)
}

func TestCatalogSyncsPipelinesOnAddPeer(t *testing.T) {
	t.Parallel()

	provider := NewServer(t, testutil.DefaultConfig(t, "pipe-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "pipe-consumer"), nil)

	pipe := protocol.PipelineSchema{
		ID: "pipe-catalog",
		Steps: []protocol.PipelineStep{
			{ID: "s1", Service: "noop"},
		},
	}
	require.NoError(t, provider.LocalPipelineAdd(string(mustMarshal(pipe))))

	linkClusterPeers(t, provider, consumer)

	require.Eventually(t, func() bool {
		_, ok := consumer.Compute.GetPipeline("pipe-catalog")
		return ok
	}, 3*time.Second, 50*time.Millisecond)
}

func TestStreamingServiceEmitsChunksAndCancelsCleanly(t *testing.T) {
	t.Parallel()

	sv := NewServer(t, testutil.DefaultConfig(t, "stream-node"), nil)
	var entered atomic.Bool
	handler := compute.ServiceHandler(func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		defer close(out)
		entered.Store(true)
		for range in {
		}
		out <- map[string]any{"n": 1}
		out <- map[string]any{"n": 2}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, nil
		}
	})
	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "streamy",
		Type: protocol.ServiceTypeBidi,
	}, handler))

	var chunks []map[string]any
	err := sv.LocalServiceStreamRun("streamy", `{"x":1}`, func(chunk map[string]any) {
		chunks = append(chunks, chunk)
	})
	require.NoError(t, err)
	require.True(t, entered.Load())
	require.Len(t, chunks, 2)
}
