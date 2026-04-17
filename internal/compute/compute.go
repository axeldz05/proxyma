package compute

import (
	"context"
	"fmt"
	"log/slog"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type ComputeEngine struct {
	taskQueue     chan protocol.TaskRequest
	registry      *ServiceRegistry
	taskStatuses  *sync.Map
	logger        *slog.Logger
	peerClient    p2p.PeerClient
	nodeID        string
	nodeAddr      string
	activeWorkers atomic.Int32
	wg 			  sync.WaitGroup
}

type registeredService struct {
	schema  protocol.ServiceSchema
	handler ServiceHandler
}

type ServiceRegistry struct {
	mu			sync.RWMutex
	services 	map[string]registeredService
}

type ServiceHandler func(ctx context.Context, payload map[string]any) (map[string]any, error)

func NewComputeEngine(logger *slog.Logger, pc p2p.PeerClient, workerCount int, id string) *ComputeEngine {
	engine := &ComputeEngine{
		taskQueue:    make(chan protocol.TaskRequest, 10),
		registry:     NewServiceRegistry(),
		taskStatuses: &sync.Map{},
		logger:       logger,
		peerClient:   pc,
		nodeID: 	  id,
	}
	for range workerCount {
		engine.wg.Add(1)
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

func (c *ComputeEngine) RegisterNewService(schema protocol.ServiceSchema, handler ServiceHandler) error {
	if err := c.registry.Register(schema, handler); err != nil {
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

// TODO: make this serviceWorker totally asynchroneous, with a limit of
// how much activeWorkers to have at the same time
func (c *ComputeEngine) serviceWorker() {
	defer c.wg.Done()
	for task := range c.taskQueue {
		c.activeWorkers.Add(1)
		c.logger.Info("Working on task...", "job_id", task.TaskID)
		
		handler, exists := c.registry.GetHandler(task.Service)
		if !exists {
			c.logger.Error("Service not found during execution", "service", task.Service)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        outputs, err := handler(ctx, task.Payload)
        cancel()

        status := "completed"
        if err != nil {
            status = "failed"
            outputs = map[string]any{"error": err.Error()}
        }		
		responsePayload := protocol.ServiceTaskResponse{
			TaskID:  task.TaskID,
			Service: task.Service,
			Status:  status,
			Outputs: outputs,
		}

		c.setTaskStatus(responsePayload)
		
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
		c.activeWorkers.Add(-1)
	}
}

func (ce *ComputeEngine) estimateTaskCost(query protocol.DiscoveryQuery) (int64, bool) {
	currentTasks := len(ce.taskQueue)
	busyWorkers := ce.activeWorkers.Load()
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
	estimatedCost += int64(busyWorkers) * 50

	activeGoroutines := runtime.NumGoroutine()
	if activeGoroutines > 100 {
		// Add 1ms penalty for every extra goroutine competing for CPU cycles
		estimatedCost += int64(activeGoroutines - 100)
	}

	return estimatedCost, true
}

func (c *ComputeEngine) SubmitTask(req protocol.TaskRequest) error {
	select {
	case c.taskQueue <- req:
		c.logger.Debug("Task accepted into queue", "taskID", req.TaskID)
		return nil
	default:
		return fmt.Errorf("node is overloaded: task queue is full")
	}
}

func (c *ComputeEngine) RegisterOutgoingTask(req protocol.TaskRequest) {
    c.setTaskStatus(protocol.ServiceTaskResponse{
        TaskID:  req.TaskID,
        Service: req.Service,
        Status:  "pending",
    })
}

func (c *ComputeEngine) MarkTaskAsFailed(taskID, service, reason string) {
    c.setTaskStatus(protocol.ServiceTaskResponse{
        TaskID:  taskID,
        Service: service,
        Status:  "failed",
        Outputs: map[string]any{"error": reason},
    })
}

func (c *ComputeEngine) setTaskStatus(response protocol.ServiceTaskResponse) {
	c.taskStatuses.Store(response.TaskID, response)
}

func (c *ComputeEngine) Close() {
	close(c.taskQueue)
	c.wg.Wait()
}
