package compute

import (
	"encoding/json"
	"net/http"
	"proxyma/internal/protocol"
)

func (s *ComputeEngine) HandleServiceBid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var query protocol.DiscoveryQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		http.Error(w, "Invalid query payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	rejectBid := func() {
        w.WriteHeader(http.StatusOK)
        bid := protocol.ServiceBid{CanAccept: false}
        if err := json.NewEncoder(w).Encode(bid); err != nil {
            s.logger.Error("failed to encode negative service bid", "error", err)
        }
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
	// TODO: El nodo deberia revisar su CPU o su cola interna de tareas en lugar de estimar.
	estimated := int64(100)
	if query.PayloadSizeBytes > 0 {
		mb := query.PayloadSizeBytes / (1024 * 1024)
		estimated += mb * 10
	}
	bid := protocol.ServiceBid{
		NodeID:          s.nodeID,
		NodeAddr:        s.nodeAddr,
		Schema:          schema,
		EstimatedMillis: estimated,
		CanAccept:       true,
	}

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(bid); err != nil {
		s.logger.Error("failed to encode positive service bid", "error", err)
	}
}

func (s *ComputeEngine) HandleServiceSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var taskReq protocol.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&taskReq); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := s.registry.ValidateRequest(taskReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"error":   "Validation failed",
			"details": err.Error(),
		}); err != nil {
			s.logger.Error("failed to encode negative validation", "error", err)
		}
		return
	}

	select {
		case s.taskQueue <- taskReq:
			w.WriteHeader(http.StatusAccepted)
			if err := json.NewEncoder(w).Encode(map[string]string{
				"status":  "accepted",
				"message": "Task received and queued for processing",
				"job_id":  taskReq.TaskID,
			}); err != nil {
				s.logger.Error("failed to encode positive validation", "error", err)
			}
			s.logger.Info("[TaskQueue] - task was queued", "taskID", taskReq.TaskID)
		default:
		    http.Error(w, "Node is overloaded", http.StatusServiceUnavailable)
		    return
	}
}

func (s *ComputeEngine) HandleServiceCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var webhookPayload protocol.ServiceTaskResponse
	if err := json.NewDecoder(r.Body).Decode(&webhookPayload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	s.taskStatuses.Store(webhookPayload.TaskID, webhookPayload)
	s.logger.Debug("Webhook received. Task updated", "job_id", webhookPayload.TaskID, "status", webhookPayload.Status)
	w.WriteHeader(http.StatusOK)
}
