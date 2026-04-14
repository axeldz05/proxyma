package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type ComputeEngine struct {
	registry     *ServiceRegistry
	taskQueue    chan TaskRequest
	taskStatuses *sync.Map
	logger       *slog.Logger
	peerClient   PeerClient
}

func NewComputeEngine(logger *slog.Logger, pc PeerClient, workerCount int) *ComputeEngine {
	engine := &ComputeEngine{
		registry:     NewServiceRegistry(),
		taskQueue:    make(chan TaskRequest, 10),
		taskStatuses: &sync.Map{},
		logger:       logger,
		peerClient:   pc,
	}
	for range workerCount {
		go engine.serviceWorker()
	}

	return engine
}

func (c *ComputeEngine) RegisterNewService(schema ServiceSchema) error {
	if err := c.registry.Register(schema); err != nil {
		c.logger.Error("[Compute Engine] - Couldn't register new service", "error", err)
		return err
	}
	return nil
}

func (c *ComputeEngine) GetTaskStatus(taskID string) (ServiceTaskResponse, bool) {
	val, exists := c.taskStatuses.Load(taskID)
	if !exists {
		return ServiceTaskResponse{}, false
	}
	res, ok := val.(ServiceTaskResponse)
	if !ok {
		return ServiceTaskResponse{}, false
	}
	return res, true
}


func (c *ComputeEngine) serviceWorker() {
	for task := range c.taskQueue {
		c.logger.Info("Working on task...", "job_id", task.TaskID)
		
		time.Sleep(500 * time.Millisecond) // Simulación
		
		responsePayload := ServiceTaskResponse{
			TaskID:  task.TaskID,
			Service: task.Service,
			Status:  "completed",
			Outputs: map[string]any{
				"text_result": "vfs://result_hash", 
			},
		}
		
		if task.ReplyTo != "" {
			body, _ := json.Marshal(responsePayload)
			req, err := http.NewRequest("POST", task.ReplyTo, bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				resp, err := c.peerClient.(*HTTPPeerClient).client.Do(req)
				if err != nil {
					c.logger.Error("Failed to deliver webhook", "job_id", task.TaskID, "error", err)
				} else {
					resp.Body.Close()
				}
			}
		} else {
			c.logger.Warn("[Compute Engine] - There's no one to reply to", "taskID", task.TaskID)
		}
	}
}
