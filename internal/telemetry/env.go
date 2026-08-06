package telemetry

import (
	"os"
	"time"
)

// InitFromEnv enables HTTP bid export when PROXYMA_OTEL_ENDPOINT or
// OTEL_EXPORTER_OTLP_ENDPOINT is set. Safe to call multiple times; no-op if unset.
func InitFromEnv() {
	endpoint := os.Getenv("PROXYMA_OTEL_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		return
	}
	SetBidExporter(NewHTTPBidExporter(endpoint, 200*time.Millisecond))
}
