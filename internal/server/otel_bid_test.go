package server_test

import (
	"context"
	"net"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/telemetry"
	"proxyma/internal/testutil"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOTelExportDisabledDoesNotBlockBid(t *testing.T) {
	t.Parallel()

	// Enabled but unreachable: blackhole TCP listener that never accepts.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	t.Cleanup(func() { _ = ln.Close() })

	restore := telemetry.SetBidExporter(telemetry.NewHTTPBidExporter("http://"+addr+"/v1/metrics", 50*time.Millisecond))
	t.Cleanup(restore)

	sv := NewServer(t, testutil.DefaultConfig(t, "otel-bid"), nil)
	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "ocr",
		Parameters: map[string]protocol.ServiceParameter{
			"file": {Type: protocol.ParamTypeFile, Required: true},
		},
	}, compute.BuildUnaryHandler(func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	const n = 32
	var wg sync.WaitGroup
	var okCount atomic.Int32
	start := time.Now()
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			bid, ok := sv.Compute.BuildServiceBid(protocol.DiscoveryQuery{Service: "ocr"})
			if ok && bid.CanAccept {
				okCount.Add(1)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bids blocked by OTel export")
	}
	require.Less(t, time.Since(start), 1500*time.Millisecond)
	require.Equal(t, int32(n), okCount.Load())
}

func TestOTelExportNoopWhenDisabled(t *testing.T) {
	t.Parallel()
	restore := telemetry.SetBidExporter(nil)
	t.Cleanup(restore)

	sv := NewServer(t, testutil.DefaultConfig(t, "otel-noop"), nil)
	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "ocr",
	}, compute.BuildUnaryHandler(func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	bid, ok := sv.Compute.BuildServiceBid(protocol.DiscoveryQuery{Service: "ocr"})
	require.True(t, ok)
	require.True(t, bid.CanAccept)
}