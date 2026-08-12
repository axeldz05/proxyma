package server_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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
	clearOTelEnv(t)

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
	clearOTelEnv(t)
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

func TestServerEnvExportsBuiltServiceBid(t *testing.T) {
	clearOTelEnv(t)
	restore := telemetry.SetBidExporter(nil)
	t.Cleanup(restore)

	type exportResult struct {
		method      string
		contentType string
		name        string
		nodeID      string
		err         error
	}
	exports := make(chan exportResult, 1)
	exportServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload struct {
			Name       string `json:"name"`
			Attributes struct {
				NodeID string `json:"node_id"`
			} `json:"attributes"`
		}
		err := json.NewDecoder(request.Body).Decode(&payload)
		exports <- exportResult{
			method:      request.Method,
			contentType: request.Header.Get("Content-Type"),
			name:        payload.Name,
			nodeID:      payload.Attributes.NodeID,
			err:         err,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(exportServer.Close)
	t.Setenv("PROXYMA_OTEL_ENDPOINT", exportServer.URL+"/v1/metrics")

	sv := NewServer(t, testutil.DefaultConfig(t, "otel-env"), nil)
	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "ocr",
	}, compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	bid, ok := sv.Compute.BuildServiceBid(protocol.DiscoveryQuery{Service: "ocr"})
	require.True(t, ok)
	require.True(t, bid.CanAccept)

	select {
	case exported := <-exports:
		require.NoError(t, exported.err)
		require.Equal(t, http.MethodPost, exported.method)
		require.Equal(t, "application/json", exported.contentType)
		require.Equal(t, "proxyma.service.bid", exported.name)
		require.Equal(t, "otel-env", exported.nodeID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for service bid export")
	}
}

func clearOTelEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PROXYMA_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
}
