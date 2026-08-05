package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"time"
)

func (s *Server) HandleServiceNotify(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.ServiceNotification](w, r)
	if !ok {
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

	s.Config.Logger.Info("Received pipeline schema notification", "pipelineID", req.Schema.ID, "action", req.Action)

	switch req.Action {
	case "add":
		if s.Storage != nil {
			_ = s.Storage.SavePipelineSchema(req.Schema)
		}
		s.Compute.RegisterPipeline(req.Schema)
	case "remove":
		if s.Storage != nil {
			_ = s.Storage.DeletePipelineSchema(req.Schema.ID)
		}
		s.Compute.UnregisterPipeline(req.Schema.ID)
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
	var msg p2p.HolePunchMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid request body")
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
			go func() {
				pingPayload := append([]byte{0xff, 0xff, 0xff, 0xff}, []byte("ping:"+s.Config.ID)...)
				// Send 20 pings, 150ms apart
				for i := 0; i < 20; i++ {
					_, _ = s.quicMgr.PacketConn.WriteTo(pingPayload, rUDPAddr)
					time.Sleep(150 * time.Millisecond)
				}
			}()
		}
	}
}

func (s *Server) HandleServicesStream(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		utils.RespondError(w, http.StatusBadRequest, "Missing service query parameter")
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
		chunkBytes, _ := json.Marshal(chunk)
		_, _ = w.Write(append(chunkBytes, '\n'))
		flusher.Flush()
	})
}
