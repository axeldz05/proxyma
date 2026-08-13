package compute

import (
	"errors"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func (s *ComputeEngine) HandleServiceBid(w http.ResponseWriter, r *http.Request) {
	query, ok := utils.DecodeJSONOrError[protocol.DiscoveryQuery](w, r)
	if !ok {
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
	bid, canAccept := s.BuildServiceBid(query)
	if !canAccept {
		rejectBid()
		return
	}
	bid.Schema = schema

	utils.RespondJSON(w, http.StatusOK, bid)
}

func (s *ComputeEngine) HandleServiceSubmit(w http.ResponseWriter, r *http.Request) {
	taskReq, ok := utils.DecodeJSONOrError[protocol.TaskRequest](w, r)
	if !ok {
		return
	}
	submitterNodeID, authenticated := p2p.RequirePeerCN(w, r)
	if !authenticated {
		return
	}
	if taskReq.PipelineState == nil {
		if taskReq.RequesterNodeID != "" && taskReq.RequesterNodeID != submitterNodeID {
			utils.RespondError(w, http.StatusForbidden, "requester node ID must match authenticated peer")
			return
		}
		taskReq.RequesterNodeID = submitterNodeID
	} else {
		expectedSubmitter := taskReq.PipelineState.OutputProducers["$initial"]
		if taskReq.PipelineState.CurrentStep > 0 {
			if schema, exists := s.GetPipeline(taskReq.Service); exists &&
				taskReq.PipelineState.CurrentStep <= len(schema.Steps) {
				previousStepID := schema.Steps[taskReq.PipelineState.CurrentStep-1].ID
				expectedSubmitter = taskReq.PipelineState.OutputProducers[previousStepID]
			}
		}
		if expectedSubmitter == "" || expectedSubmitter != submitterNodeID {
			utils.RespondError(w, http.StatusForbidden, "pipeline provenance does not match authenticated submitter")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")

	if _, isPipeline := s.GetPipeline(taskReq.Service); !isPipeline {
		if err := s.registry.ValidateRequest(taskReq); err != nil {
			utils.RespondError(w, http.StatusBadRequest, "Validation failed: "+err.Error())
			return
		}
	}
	if err := s.SubmitTask(taskReq); err != nil {
		utils.RespondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	utils.RespondJSON(w, http.StatusAccepted, protocol.APITaskAck{
		Status:  "accepted",
		Message: "Task received and queued for processing",
		JobID:   taskReq.TaskID,
	})
}

func (s *ComputeEngine) HandleServiceCallback(w http.ResponseWriter, r *http.Request) {
	webhookPayload, ok := utils.DecodeJSONOrError[protocol.ServiceTaskResponse](w, r)
	if !ok {
		return
	}
	producerNodeID, authenticated := p2p.RequirePeerCN(w, r)
	if !authenticated {
		return
	}
	if webhookPayload.ProducerNodeID != "" && webhookPayload.ProducerNodeID != producerNodeID {
		utils.RespondError(w, http.StatusForbidden, "producer node ID must match authenticated peer")
		return
	}
	if err := s.AcceptTaskCallback(producerNodeID, webhookPayload); err != nil {
		switch {
		case errors.Is(err, ErrTaskProducer):
			utils.RespondError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, ErrTaskNotFound):
			utils.RespondError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrTaskTerminal):
			utils.RespondError(w, http.StatusConflict, err.Error())
		default:
			utils.RespondError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	s.logger.Debug("Webhook received. Task updated", "job_id", webhookPayload.TaskID, "status", webhookPayload.Status)
	utils.RespondJSON(w, http.StatusOK, protocol.APITaskAck{
		Status:  "ok",
		Message: "Webhook received",
		JobID:   webhookPayload.TaskID,
	})
}
