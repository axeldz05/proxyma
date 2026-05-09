package compute

import (
	"encoding/json"
	"net/http"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *ComputeEngine) HandleServiceBid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var query protocol.DiscoveryQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid query payload")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	rejectBid := func() {
		bid := protocol.ServiceBid{CanAccept: false}
		utils.RespondJSON(w, http.StatusOK, bid)
	}
	schema, exists := s.registry.Get(query.Service)
	if !exists {
		rejectBid()
		return
	}

	for _, reqParam := range query.RequiredParams {
		if _, hasParam := schema.Parameters[reqParam]; !hasParam {
			rejectBid()
			return
		}
	}
	estimated, canAccept := s.estimateTaskCost(query)
	if !canAccept {
		rejectBid()
		return
	}
	bid := protocol.ServiceBid{
		NodeID:          s.nodeID,
		NodeAddr:        s.nodeAddr,
		Schema:          schema,
		EstimatedMillis: estimated,
		CanAccept:       true,
	}

	utils.RespondJSON(w, http.StatusOK, bid)
}

func (s *ComputeEngine) HandleServiceSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var taskReq protocol.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&taskReq); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := s.registry.ValidateRequest(taskReq); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Validation failed: "+err.Error())
		return
	}
	if err := s.SubmitTask(taskReq); err != nil {
		utils.RespondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	utils.RespondJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"message": "Task received and queued for processing",
		"job_id":  taskReq.TaskID,
	})
}

func (s *ComputeEngine) HandleServiceCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var webhookPayload protocol.ServiceTaskResponse
	if err := json.NewDecoder(r.Body).Decode(&webhookPayload); err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	s.taskStatuses.Store(webhookPayload.TaskID, webhookPayload)
	s.logger.Debug("Webhook received. Task updated", "job_id", webhookPayload.TaskID, "status", webhookPayload.Status)
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Webhook received",
		"job_id":  webhookPayload.TaskID,
	})
}
