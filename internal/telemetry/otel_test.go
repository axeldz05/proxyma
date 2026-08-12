package telemetry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testWaitTimeout = 2 * time.Second

func TestSetBidExporterReplacesAndRestoresExporter(t *testing.T) {
	isolateBidExporter(t)

	first := make(chan protocol.ServiceBid, 1)
	restoreFirst := SetBidExporter(func(bid protocol.ServiceBid) {
		first <- bid
	})
	t.Cleanup(restoreFirst)

	second := make(chan protocol.ServiceBid, 1)
	restoreSecond := SetBidExporter(func(bid protocol.ServiceBid) {
		second <- bid
	})
	t.Cleanup(restoreSecond)

	want := testBid()
	ExportBidAsync(want)
	require.Equal(t, want, awaitBid(t, second))

	restoreSecond()
	ExportBidAsync(want)
	require.Equal(t, want, awaitBid(t, first))
}

func TestExportBidAsyncNoopWhenDisabled(t *testing.T) {
	isolateBidExporter(t)

	var calls atomic.Int32
	restoreEnabled := SetBidExporter(func(protocol.ServiceBid) {
		calls.Add(1)
	})
	restoreDisabled := SetBidExporter(nil)
	t.Cleanup(restoreEnabled)
	t.Cleanup(restoreDisabled)

	ExportBidAsync(testBid())

	require.Zero(t, calls.Load())
	restoreDisabled()
}

func TestNewHTTPBidExporterPostsJSON(t *testing.T) {
	type capturedRequest struct {
		method      string
		contentType string
		body        []byte
		err         error
	}

	requests := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		requests <- capturedRequest{
			method:      request.Method,
			contentType: request.Header.Get("Content-Type"),
			body:        body,
			err:         err,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	exporter := NewHTTPBidExporter(server.URL+"/v1/metrics", time.Second)
	require.NotNil(t, exporter)
	exporter(testBid())

	captured := awaitRequest(t, requests)
	require.NoError(t, captured.err)
	require.Equal(t, http.MethodPost, captured.method)
	require.Equal(t, "application/json", captured.contentType)

	var payload struct {
		Name       string `json:"name"`
		Attributes struct {
			NodeID          string  `json:"node_id"`
			EstimatedMillis int64   `json:"estimated_millis"`
			CPULoad         float64 `json:"cpu_load"`
			MemPressure     float64 `json:"mem_pressure"`
			CostUnits       int64   `json:"cost_units"`
			PowerScore      int64   `json:"power_score"`
			CanAccept       bool    `json:"can_accept"`
		} `json:"attributes"`
	}
	require.NoError(t, json.Unmarshal(captured.body, &payload))
	require.Equal(t, "proxyma.service.bid", payload.Name)
	require.Equal(t, "node-1", payload.Attributes.NodeID)
	require.Equal(t, int64(125), payload.Attributes.EstimatedMillis)
	require.Equal(t, 0.25, payload.Attributes.CPULoad)
	require.Equal(t, 0.5, payload.Attributes.MemPressure)
	require.Equal(t, int64(275), payload.Attributes.CostUnits)
	require.Equal(t, int64(300), payload.Attributes.PowerScore)
	require.True(t, payload.Attributes.CanAccept)
}

func TestNewHTTPBidExporterEmptyEndpointIsDisabled(t *testing.T) {
	require.Nil(t, NewHTTPBidExporter("", time.Second))
}

func TestHTTPBidExporterTimeoutIsBounded(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerFinished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestStarted)
		<-releaseHandler
		close(handlerFinished)
	}))
	t.Cleanup(server.Close)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHandler) })
	}
	t.Cleanup(release)

	exporter := NewHTTPBidExporter(server.URL, 25*time.Millisecond)
	finished := make(chan struct{})
	go func() {
		exporter(testBid())
		close(finished)
	}()

	awaitRequest(t, requestStarted)
	awaitRequest(t, finished)
	release()
	awaitRequest(t, handlerFinished)
}

func TestExportBidAsyncDoesNotBlock(t *testing.T) {
	isolateBidExporter(t)

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	restore := SetBidExporter(func(protocol.ServiceBid) {
		close(started)
		<-release
	})
	t.Cleanup(restore)

	returned := make(chan struct{})
	go func() {
		ExportBidAsync(testBid())
		close(returned)
	}()

	awaitRequest(t, started)
	awaitRequest(t, returned)
}

func TestExportBidAsyncIsolatesExporterPanic(t *testing.T) {
	isolateBidExporter(t)

	panicked := make(chan struct{})
	restorePanicking := SetBidExporter(func(protocol.ServiceBid) {
		defer close(panicked)
		panic(errors.New("export failure"))
	})
	t.Cleanup(restorePanicking)

	ExportBidAsync(testBid())
	awaitRequest(t, panicked)

	healthy := make(chan protocol.ServiceBid, 1)
	restoreHealthy := SetBidExporter(func(bid protocol.ServiceBid) {
		healthy <- bid
	})
	t.Cleanup(restoreHealthy)

	want := testBid()
	ExportBidAsync(want)
	require.Equal(t, want, awaitBid(t, healthy))
}

func isolateBidExporter(t *testing.T) {
	t.Helper()
	restore := SetBidExporter(nil)
	t.Cleanup(restore)
}

func testBid() protocol.ServiceBid {
	return protocol.ServiceBid{
		NodeID:          "node-1",
		NodeAddr:        "https://127.0.0.1:8080",
		EstimatedMillis: 125,
		CPULoad:         0.25,
		MemPressure:     0.5,
		CostUnits:       275,
		PowerScore:      300,
		CanAccept:       true,
		Capabilities: map[string]int{
			protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
		},
	}
}

func awaitBid(t *testing.T, bids <-chan protocol.ServiceBid) protocol.ServiceBid {
	t.Helper()
	return awaitRequest(t, bids)
}

func awaitRequest[T any](t *testing.T, requests <-chan T) T {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for telemetry event")
		var zero T
		return zero
	}
}
