package server

import (
	"context"
	"io"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	decodeNotifyOK(w, r, func(req protocol.ServiceNotification) {
		if !s.Storage.IsServiceSubscribed(req.Schema.Name) {
			s.Config.Logger.Debug("Ignoring unsolicited service notify", "service", req.Schema.Name, "peer", req.NodeID)
			return
		}
		s.Peers.UpdatePeerService(req.NodeID, req.Action, req.Schema)
	})
}

func (s *Server) HandleSchemaNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.PipelineNotification](w, r)
	if !ok {
		return
	}
	s.Config.Logger.Info("Received pipeline schema notification", "pipelineID", req.Schema.ID, "action", req.Action)
	if req.Action == protocol.ActionAdd {
		if err := s.ValidatePipelineSchema(req.Schema); err != nil {
			s.Config.Logger.Warn("Rejecting invalid pipeline schema from peer", "pipelineID", req.Schema.ID, "error", err)
			utils.RespondError(w, http.StatusBadRequest, "invalid pipeline schema: "+err.Error())
			return
		}
	}
	if err := s.applyPipelineAction(req.Schema, req.Action); err != nil {
		utils.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
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

	// Callee-side punch: same dialer rule as initiator (lower ID dials).
	if s.quicMgr != nil && msg.PublicUDP != "" {
		go s.quicMgr.RespondToHolePunch(context.Background(), msg.SenderID, msg.PublicUDP)
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
