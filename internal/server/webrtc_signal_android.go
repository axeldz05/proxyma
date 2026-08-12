//go:build android

package server

import (
	"net/http"

	"proxyma/internal/utils"
)

// HandleWebRTCSignal keeps the signaling route explicit on Android instead of
// silently falling back to another transport.
func (*Server) HandleWebRTCSignal(w http.ResponseWriter, _ *http.Request) {
	utils.RespondError(w, http.StatusNotImplemented, "webrtc signaling is unsupported on Android builds")
}

func (*Server) closeWebRTCPeers() {}
