package server

import (
	"io"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
)

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.ServiceNotification](w, r)
	if !ok {
		return
	}
	if !requirePeerCNMatchesBodyID(w, r, req.NodeID) {
		return
	}
	subscribed, err := s.Storage.IsServiceSubscribedE(req.Schema.Name)
	if err != nil {
		utils.RespondError(w, http.StatusInternalServerError, "failed to read service subscriptions")
		return
	}
	if !subscribed {
		s.Config.Logger.Debug("Ignoring unsolicited service notify", "service", req.Schema.Name, "peer", req.NodeID)
		w.WriteHeader(http.StatusOK)
		return
	}
	s.Peers.UpdatePeerService(req.NodeID, req.Action, req.Schema)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleSchemaNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.PipelineNotification](w, r)
	if !ok {
		return
	}
	if !requirePeerCNMatchesBodyID(w, r, req.NodeID) {
		return
	}
	req.Schema = protocol.NormalizePipelineSchemaVersion(req.Schema)
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
	if !requirePeerCNMatchesBodyID(w, r, msg.SenderID) {
		return
	}

	s.Config.Logger.Info("Received hole punch initialization request", "sender", msg.SenderID, "senderUDP", msg.PublicUDP)

	natState := s.CurrentNATState()
	// Respond with our own public UDP address
	resp := p2p.HolePunchMessage{
		SenderID:  s.Config.ID,
		PublicUDP: natState.PublicUDPAddr,
	}

	utils.RespondJSON(w, http.StatusOK, resp)

	// Callee-side punch: same dialer rule as initiator (lower ID dials).
	if natState.QUICManager != nil && msg.PublicUDP != "" {
		s.goOwned(func() {
			natState.QUICManager.RespondToHolePunch(s.lifetimeCtx, msg.SenderID, msg.PublicUDP)
		})
	}
}

func (s *Server) HandleServicesStream(w http.ResponseWriter, r *http.Request) {
	serviceName, ok := utils.GetRequiredQueryParam(w, r, protocol.QueryService)
	if !ok {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	payload, err := parseServicePayload(string(bodyBytes))
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		utils.RespondError(w, http.StatusInternalServerError, "Streaming unsupported on connection")
		return
	}

	streamVersion := 0
	for _, advertised := range strings.Split(r.Header.Get(protocol.HeaderStreamAcceptVersions), ",") {
		if strings.TrimSpace(advertised) == "1" {
			streamVersion = protocol.ServiceStreamVersion
			break
		}
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if streamVersion == protocol.ServiceStreamVersion {
		w.Header().Set(protocol.HeaderStreamSelectedVersion, "1")
	} else {
		w.Header().Set(protocol.HeaderStreamSelectedVersion, protocol.StreamVersionLegacy)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	streamCtx, cancel := s.contextWithServerLifetime(r.Context())
	defer cancel()
	err = s.localServiceStreamRun(streamCtx, serviceName, payload, func(chunk map[string]any) error {
		frame := any(chunk)
		if streamVersion == protocol.ServiceStreamVersion {
			frame = protocol.NewServiceStreamChunk(chunk)
		}
		if err := utils.WriteNDJSON(w, frame); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})

	if streamVersion == protocol.ServiceStreamVersion {
		terminal := protocol.NewServiceStreamTerminal(protocol.ServiceStreamFrameComplete, "")
		if err != nil {
			terminal = protocol.NewServiceStreamTerminal(protocol.ServiceStreamFrameError, err.Error())
		}
		if writeErr := utils.WriteNDJSON(w, terminal); writeErr == nil {
			flusher.Flush()
		}
	}
}
