package telemetry

import (
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitFromEnvUnsetPreservesCurrentExporter(t *testing.T) {
	isolateBidExporter(t)
	t.Setenv("PROXYMA_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	exported := make(chan protocol.ServiceBid, 1)
	restore := SetBidExporter(func(bid protocol.ServiceBid) {
		exported <- bid
	})
	t.Cleanup(restore)

	InitFromEnv()
	want := testBid()
	ExportBidAsync(want)

	require.Equal(t, want, awaitBid(t, exported))
}

func TestInitFromEnvPrefersProxymaEndpoint(t *testing.T) {
	isolateBidExporter(t)

	proxymaRequests, proxymaCount, proxymaServer := newEnvTestSink(t)
	_, otlpCount, otlpServer := newEnvTestSink(t)
	t.Setenv("PROXYMA_OTEL_ENDPOINT", proxymaServer.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", otlpServer.URL)

	InitFromEnv()
	ExportBidAsync(testBid())

	awaitRequest(t, proxymaRequests)
	require.Equal(t, int32(1), proxymaCount.Load())
	require.Zero(t, otlpCount.Load())
}

func TestInitFromEnvFallsBackToOTLPEndpoint(t *testing.T) {
	isolateBidExporter(t)

	requests, count, server := newEnvTestSink(t)
	t.Setenv("PROXYMA_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)

	InitFromEnv()
	ExportBidAsync(testBid())

	awaitRequest(t, requests)
	require.Equal(t, int32(1), count.Load())
}

func TestInitFromEnvReplacesCurrentExporter(t *testing.T) {
	isolateBidExporter(t)

	var previousCalls atomic.Int32
	restore := SetBidExporter(func(protocol.ServiceBid) {
		previousCalls.Add(1)
	})
	t.Cleanup(restore)

	requests, count, server := newEnvTestSink(t)
	t.Setenv("PROXYMA_OTEL_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	InitFromEnv()
	ExportBidAsync(testBid())

	awaitRequest(t, requests)
	require.Equal(t, int32(1), count.Load())
	require.Zero(t, previousCalls.Load())
}

func newEnvTestSink(t *testing.T) (<-chan struct{}, *atomic.Int32, *httptest.Server) {
	t.Helper()

	requests := make(chan struct{}, 1)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		requests <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return requests, &count, server
}
