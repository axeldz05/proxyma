package p2p

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyma/internal/protocol"
)

func TestHTTPPeerClientNegotiatesServiceStreamVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		selectedVersion string
		wantVersion     int
		wantError       bool
	}{
		{name: "v1", selectedVersion: "1", wantVersion: protocol.ServiceStreamVersion},
		{name: "legacy fallback", selectedVersion: "", wantVersion: 0},
		{name: "unsupported selection", selectedVersion: "2", wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(protocol.HeaderStreamAcceptVersions); got != "1" {
					t.Errorf("advertised versions = %q, want 1", got)
				}
				if test.selectedVersion != "" {
					w.Header().Set(protocol.HeaderStreamSelectedVersion, test.selectedVersion)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "{\"n\":1}\n")
			}))
			t.Cleanup(server.Close)

			client := NewHTTPPeerClient(
				http.DefaultTransport,
				"",
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			t.Cleanup(client.Close)
			stream, err := client.StreamServiceNegotiated(
				context.Background(),
				server.URL,
				"stream",
				map[string]any{},
			)
			if test.wantError {
				if err == nil {
					_ = stream.Body.Close()
					t.Fatal("unsupported selected version was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("negotiate stream: %v", err)
			}
			defer func() { _ = stream.Body.Close() }()
			if stream.Version != test.wantVersion {
				t.Fatalf("selected version = %d, want %d", stream.Version, test.wantVersion)
			}
		})
	}
}
