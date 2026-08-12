//go:build android

package androidcontract_test

import (
	"testing"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
)

// Android intentionally exposes only the shared BuildHandler entry point, not
// Pion-backed helper APIs. Registration must reject WebRTC before returning a
// handler that could be persisted or executed later.
func TestWebRTCRegistrationFailsImmediately(t *testing.T) {
	handler, err := compute.BuildHandler(protocol.ServiceTypeWebRTC, "https://peer.example/webrtc/signal")
	if handler != nil {
		t.Fatal("Android WebRTC registration returned an unusable handler")
	}
	if err == nil {
		t.Fatal("Android WebRTC registration unexpectedly succeeded")
	}
	const want = "webrtc services are unsupported on Android builds"
	if err.Error() != want {
		t.Fatalf("Android WebRTC registration error = %q, want %q", err, want)
	}
}
