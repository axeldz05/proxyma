// Package telemetry provides optional best-effort export of mesh metrics.
// Export failures never block bid/compute paths.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"proxyma/internal/protocol"
	"sync"
	"time"
)

// BidExporter ships a ServiceBid snapshot asynchronously (best-effort).
type BidExporter func(bid protocol.ServiceBid)

var (
	mu          sync.RWMutex
	bidExporter BidExporter
	logger      = slog.Default()
)

// SetBidExporter replaces the active bid exporter. Pass nil for no-op (disabled).
// Returns a restore func for tests.
func SetBidExporter(fn BidExporter) (restore func()) {
	mu.Lock()
	prev := bidExporter
	bidExporter = fn
	mu.Unlock()
	return func() {
		mu.Lock()
		bidExporter = prev
		mu.Unlock()
	}
}

// ExportBidAsync fires the configured exporter without blocking the caller.
func ExportBidAsync(bid protocol.ServiceBid) {
	mu.RLock()
	fn := bidExporter
	mu.RUnlock()
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Debug("otel bid export panic", "recover", r)
			}
		}()
		fn(bid)
	}()
}

// NewHTTPBidExporter POSTs JSON bid metrics to endpoint. Timeout bounds each attempt.
// Unreachable endpoints only log; they must not stall the caller (ExportBidAsync).
func NewHTTPBidExporter(endpoint string, timeout time.Duration) BidExporter {
	if endpoint == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	return func(bid protocol.ServiceBid) {
		payload, err := json.Marshal(map[string]any{
			"name": "proxyma.service.bid",
			"attributes": map[string]any{
				"node_id":          bid.NodeID,
				"estimated_millis": bid.EstimatedMillis,
				"cpu_load":         bid.CPULoad,
				"mem_pressure":     bid.MemPressure,
				"cost_units":       bid.CostUnits,
				"power_score":      bid.PowerScore,
				"can_accept":       bid.CanAccept,
			},
		})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			logger.Debug("otel bid export failed", "endpoint", endpoint, "error", err)
			return
		}
		_ = resp.Body.Close()
	}
}
