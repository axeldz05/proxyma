//go:build android || androidcontract

package androidcontract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

// Android keeps the signaling endpoint present but explicitly unsupported.
// This must not accept an offer or silently select another transport.
func TestWebRTCSignalingReturnsUnsupported(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, protocol.PathWebRTCSignal, nil)
	response := httptest.NewRecorder()

	(&server.Server{}).HandleWebRTCSignal(response, request)

	if response.Code != http.StatusNotImplemented {
		t.Fatalf("Android WebRTC signaling status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Android WebRTC signaling Content-Type = %q, want application/json", contentType)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode Android WebRTC signaling response: %v", err)
	}
	const want = "webrtc signaling is unsupported on Android builds"
	if payload.Error != want {
		t.Fatalf("Android WebRTC signaling error = %q, want %q", payload.Error, want)
	}
}
