package compute

import (
	"context"
	"log/slog"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"runtime"
	"sync"
	"time"
)

type ComputeEngine struct {
	registry     *ServiceRegistry
	taskQueue    chan protocol.TaskRequest
	taskStatuses *sync.Map
	logger       *slog.Logger
	peerClient   p2p.PeerClient
	nodeID       string
	nodeAddr     string
}

type ServiceRegistry struct {
	mu		sync.RWMutex
	schemas map[string]protocol.ServiceSchema
}

func NewComputeEngine(logger *slog.Logger, pc p2p.PeerClient, workerCount int, id string) *ComputeEngine {
	engine := &ComputeEngine{
		registry:     NewServiceRegistry(),
		taskQueue:    make(chan protocol.TaskRequest, 10),
		taskStatuses: &sync.Map{},
		logger:       logger,
		peerClient:   pc,
		nodeID: 	  id,
	}
	for range workerCount {
		go engine.serviceWorker()
	}

	return engine
}

func (c *ComputeEngine) SetAddress(addr string) {
	c.nodeAddr = addr
}

func (c *ComputeEngine) GetService(serviceName string) (protocol.ServiceSchema, bool){
	return c.registry.Get(serviceName)
}

func (c *ComputeEngine) RegisterNewService(schema protocol.ServiceSchema) error {
	if err := c.registry.Register(schema); err != nil {
		c.logger.Error("[Compute Engine] - Couldn't register new service", "error", err)
		return err
	}
	return nil
}

func (c *ComputeEngine) GetTaskStatus(taskID string) (protocol.ServiceTaskResponse, bool) {
	val, exists := c.taskStatuses.Load(taskID)
	if !exists {
		return protocol.ServiceTaskResponse{}, false
	}
	res, ok := val.(protocol.ServiceTaskResponse)
	if !ok {
		return protocol.ServiceTaskResponse{}, false
	}
	return res, true
}

func (c *ComputeEngine) SetTaskStatusByID(taskID string, response protocol.ServiceTaskResponse) {
	c.taskStatuses.Store(taskID, response)
}

func (c *ComputeEngine) serviceWorker() {
	for task := range c.taskQueue {
		c.logger.Info("Working on task...", "job_id", task.TaskID)
		
		time.Sleep(500 * time.Millisecond) // Simulación
		
		responsePayload := protocol.ServiceTaskResponse{
			TaskID:  task.TaskID,
			Service: task.Service,
			Status:  "completed",
			Outputs: map[string]any{
				"text_result": "vfs://result_hash", 
			},
		}
		
		if task.ReplyTo != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            err := c.peerClient.SendTaskResponse(ctx, task.ReplyTo, responsePayload)
			cancel()
			if err != nil {
				c.logger.Error("Failed to deliver webhook", "job_id", task.TaskID, "error", err)
			}
		} else {
			c.logger.Warn("[Compute Engine] - There's no one to reply to", "taskID", task.TaskID)
		}
	}
}

func (ce *ComputeEngine) estimateTaskCost(query protocol.DiscoveryQuery) (int64, bool) {
	currentTasks := len(ce.taskQueue)
	maxTasks := cap(ce.taskQueue)
	
	if maxTasks > 0 && float64(currentTasks)/float64(maxTasks) > 0.9 {
		ce.logger.Warn("Node overloaded, rejecting task bid", "queue_length", currentTasks)
		return 0, false
	}

	var estimatedCost int64 = 100 // Base latency penalty in ms
	
	if query.PayloadSizeBytes > 0 {
		mb := query.PayloadSizeBytes / (1024 * 1024)
		estimatedCost += mb * 10
	}

	// Add a penalty for each task already waiting in line. 
	// Assuming an average task takes 50ms.
	estimatedCost += int64(currentTasks) * 50

	activeGoroutines := runtime.NumGoroutine()
	if activeGoroutines > 100 {
		// Add 1ms penalty for every extra goroutine competing for CPU cycles
		estimatedCost += int64(activeGoroutines - 100)
	}

	return estimatedCost, true
}

func (c *ComputeEngine) Close() {
	close(c.taskQueue)
}
