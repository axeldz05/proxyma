//go:build android

package compute

import "errors"

var errWebRTCUnsupported = errors.New("webrtc services are unsupported on Android builds")

// buildWebRTCService is the only WebRTC API Android needs. Pion-facing helper
// APIs intentionally exist only in webrtc_handler.go's non-Android build.
// Registration fails here so an unusable service is never persisted.
func buildWebRTCService(string) (ServiceHandler, error) {
	return nil, errWebRTCUnsupported
}

// Compile-time contract shared with serviceTypeBuilders without importing Pion.
var _ func(string) (ServiceHandler, error) = buildWebRTCService
