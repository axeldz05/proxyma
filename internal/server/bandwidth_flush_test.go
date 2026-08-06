package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBandwidthWrapperPreservesFlusher(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	cw := &countingResponseWriter{ResponseWriter: rec}
	flusher, ok := any(cw).(http.Flusher)
	require.True(t, ok, "countingResponseWriter must expose http.Flusher for NDJSON streams")
	require.NotPanics(t, flusher.Flush)
}
