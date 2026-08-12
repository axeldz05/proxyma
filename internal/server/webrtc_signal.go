//go:build !android && !androidcontract

package server

import (
	"net/http"
	"proxyma/internal/compute"
	"proxyma/internal/utils"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

// HandleWebRTCSignal answers an offer over mTLS and keeps the PeerConnection
// alive for DataChannel traffic (echo JSON text).
func (s *Server) HandleWebRTCSignal(w http.ResponseWriter, r *http.Request) {
	offer, ok := utils.DecodeJSONOrError[webrtc.SessionDescription](w, r)
	if !ok {
		return
	}
	pc, answer, err := compute.AcceptWebRTCOfferEcho(offer)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.trackWebRTCPeer(pc)
	utils.RespondJSON(w, http.StatusOK, answer)
}

func (s *Server) trackWebRTCPeer(pc *webrtc.PeerConnection) {
	id := atomic.AddUint64(&s.webrtcPCSeq, 1)
	s.webrtcPCs.Store(id, pc)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateDisconnected:
			_ = pc.Close()
			s.webrtcPCs.Delete(id)
		}
	})
}

func (s *Server) closeWebRTCPeers() {
	s.webrtcPCs.Range(func(key, value any) bool {
		if pc, ok := value.(*webrtc.PeerConnection); ok {
			_ = pc.Close()
		}
		s.webrtcPCs.Delete(key)
		return true
	})
}
