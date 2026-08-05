package server

import (
	"io"
	"net"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"time"
)

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	decodeNotifyOK(w, r, func(req protocol.ServiceNotification) {
		s.Peers.UpdatePeerService(req.NodeID, req.Action, req.Schema)
	})
}

func (s *Server) HandleSchemaNotify(w http.ResponseWriter, r *http.Request) {
	decodeNotifyOK(w, r, func(req protocol.PipelineNotification) {
		s.Config.Logger.Info("Received pipeline schema notification", "pipelineID", req.Schema.ID, "action", req.Action)
		_ = s.applyPipelineAction(req.Schema, req.Action)
	})
}

func decodeNotifyOK[T any](w http.ResponseWriter, r *http.Request, fn func(T)) {
	req, ok := utils.DecodeJSONOrError[T](w, r)
	if !ok {
		return
	}
	fn(req)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleTelemetry(w http.ResponseWriter, r *http.Request) {
	memLimit := utils.ReadMemoryLimit()
	cpuLimit := utils.ReadCPULimit()

	res := map[string]any{
		"node_id":      s.Config.ID,
		"cpu_limit":    cpuLimit,
		"memory_limit": memLimit,
	}

	w.Header().Set("Content-Type", "application/json")
	utils.RespondJSON(w, http.StatusOK, res)
}

func (s *Server) HandleGetServices(w http.ResponseWriter, r *http.Request) {
	utils.RespondJSON(w, http.StatusOK, s.Compute.ListServices())
}

func (s *Server) HandleHolePunchInit(w http.ResponseWriter, r *http.Request) {
	msg, ok := utils.DecodeJSONOrError[p2p.HolePunchMessage](w, r)
	if !ok {
		return
	}

	s.Config.Logger.Info("Received hole punch initialization request", "sender", msg.SenderID, "senderUDP", msg.PublicUDP)

	// Respond with our own public UDP address
	resp := p2p.HolePunchMessage{
		SenderID:  s.Config.ID,
		PublicUDP: s.publicUDPAddr,
	}

	utils.RespondJSON(w, http.StatusOK, resp)

	// Start pinging A in a background goroutine
	if s.quicMgr != nil && msg.PublicUDP != "" {
		rUDPAddr, err := net.ResolveUDPAddr("udp", msg.PublicUDP)
		if err == nil {
			go p2p.BurstPings(s.quicMgr.PacketConn, rUDPAddr, s.Config.ID, 20, 150*time.Millisecond)
		}
	}
}

func (s *Server) HandleServicesStream(w http.ResponseWriter, r *http.Request) {
	serviceName, ok := utils.GetRequiredQueryParam(w, r, "service")
	if !ok {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		utils.RespondError(w, http.StatusInternalServerError, "Streaming unsupported on connection")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	_ = s.LocalServiceStreamRun(serviceName, string(bodyBytes), func(chunk map[string]any) {
		_ = utils.WriteNDJSON(w, chunk)
		flusher.Flush()
	})
}
