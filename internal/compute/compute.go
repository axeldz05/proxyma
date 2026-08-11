package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/telemetry"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

type ComputeEngine struct {
	taskQueue       chan protocol.TaskRequest
	registry        *ServiceRegistry
	taskStatuses    *sync.Map
	logger          *slog.Logger
	peerClient      p2p.PeerClient
	nodeID          string
	nodeAddr        string
	activeWorkers   atomic.Int32
	wg              sync.WaitGroup
	pipelines       map[string]protocol.PipelineSchema
	pipelinesMu     sync.RWMutex
	serviceFinder   ServiceFinder
	taskDispatcher  TaskDispatcher
	vfsBlobResolver VFSBlobResolver
	vfsBlobStager   VFSBlobStager
}

type VFSBlobStager func(path string) (string, int64, error)
type VFSBlobResolver func(ctx context.Context, requesterNodeID, hash string) (string, error)
type ServiceFinder func(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error)
type TaskDispatcher func(targetPeerID string, req protocol.TaskRequest) error

type registeredService struct {
	schema  protocol.ServiceSchema
	handler ServiceHandler
}

type ServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]registeredService
}

type ServiceHandler func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error)

func (h ServiceHandler) Execute(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if h == nil {
		return nil, fmt.Errorf("service handler is nil")
	}
	return h(ctx, nil, nil, payload)
}

func (h ServiceHandler) ExecuteStream(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any) error {
	if h == nil {
		return fmt.Errorf("service handler is nil")
	}
	_, err := h(ctx, in, out, nil)
	return err
}

func NewComputeEngine(logger *slog.Logger, pc p2p.PeerClient, workerCount int, id string) *ComputeEngine {
	engine := &ComputeEngine{
		taskQueue:    make(chan protocol.TaskRequest, 10),
		registry:     NewServiceRegistry(),
		taskStatuses: &sync.Map{},
		logger:       logger,
		peerClient:   pc,
		nodeID:       id,
		pipelines:    make(map[string]protocol.PipelineSchema),
	}
	engine.wg.Add(1)
	go engine.serviceWorker(workerCount)

	return engine
}

func (c *ComputeEngine) SetAddress(addr string) {
	c.nodeAddr = addr
}

func (c *ComputeEngine) GetService(serviceName string) (protocol.ServiceSchema, bool) {
	return c.registry.Get(serviceName)
}

func (c *ComputeEngine) GetHandler(serviceName string) (ServiceHandler, bool) {
	return c.registry.GetHandler(serviceName)
}

func (c *ComputeEngine) ListServices() []string {
	return c.registry.ListAll()
}

func (c *ComputeEngine) RegisterNewService(schema protocol.ServiceSchema, handler ServiceHandler) error {
	if err := c.registry.Register(schema, handler); err != nil {
		c.logger.Error("[Compute Engine] - Couldn't register new service", "error", err)
		return err
	}
	return nil
}

func (c *ComputeEngine) RegisterPipeline(schema protocol.PipelineSchema) {
	c.pipelinesMu.Lock()
	defer c.pipelinesMu.Unlock()
	c.pipelines[schema.ID] = schema
	c.logger.Info("Pipeline schema registered", "pipelineID", schema.ID)
}

func (c *ComputeEngine) UnregisterPipeline(id string) {
	c.pipelinesMu.Lock()
	defer c.pipelinesMu.Unlock()
	delete(c.pipelines, id)
	c.logger.Info("Pipeline schema unregistered", "pipelineID", id)
}

func (c *ComputeEngine) GetPipeline(id string) (protocol.PipelineSchema, bool) {
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	schema, exists := c.pipelines[id]
	return schema, exists
}

func (c *ComputeEngine) ListPipelines() []protocol.PipelineSchema {
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	list := make([]protocol.PipelineSchema, 0, len(c.pipelines))
	for _, schema := range c.pipelines {
		list = append(list, schema)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (c *ComputeEngine) SetServiceFinder(finder ServiceFinder) {
	c.serviceFinder = finder
}

func (c *ComputeEngine) SetTaskDispatcher(dispatcher TaskDispatcher) {
	c.taskDispatcher = dispatcher
}

func (c *ComputeEngine) SetVFSBlobResolver(resolver VFSBlobResolver) {
	c.vfsBlobResolver = resolver
}

func (c *ComputeEngine) SetVFSBlobStager(stager VFSBlobStager) {
	c.vfsBlobStager = stager
}

func (c *ComputeEngine) GetTaskResponse(taskID string) (protocol.ServiceTaskResponse, bool) {
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

func (c *ComputeEngine) serviceWorker(maxWorkers int) {
	defer c.wg.Done()
	sem := make(chan struct{}, maxWorkers)
	for task := range c.taskQueue {
		sem <- struct{}{}
		c.wg.Add(1)
		go func(t protocol.TaskRequest) {
			defer c.wg.Done()
			defer func() { <-sem }()
			c.processTask(t)
		}(task)
	}
}

func (c *ComputeEngine) processTask(t protocol.TaskRequest) {
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Add(-1)

	if t.Payload == nil {
		t.Payload = make(map[string]any)
	}

	// Intercept and run as pipeline if it is a pipeline schema
	pipelineSchema, isPipeline := c.GetPipeline(t.Service)
	if !isPipeline {
		// Treat singular task as a pipeline of one step
		pipelineSchema = protocol.PipelineSchema{
			ID:      t.Service,
			Version: 1,
			Steps: []protocol.PipelineStep{
				{
					ID:      "step0",
					Service: t.Service,
				},
			},
		}
	}

	c.executePipelineStep(t, pipelineSchema)
}

func (c *ComputeEngine) executePipelineStep(t protocol.TaskRequest, schema protocol.PipelineSchema) {
	pipelineCtx := loadPipelineCtx(t.Payload)
	if pipelineCtx.Outputs == nil {
		pipelineCtx.Outputs = make(map[string]map[string]any)
	}

	if pipelineCtx.CurrentStep < 0 || pipelineCtx.CurrentStep >= len(schema.Steps) {
		c.logger.Error("Pipeline step index out of bounds", "step", pipelineCtx.CurrentStep, "total", len(schema.Steps))
		c.sendPipelineError(t, fmt.Errorf("step index %d out of bounds", pipelineCtx.CurrentStep))
		return
	}

	currentStep := schema.Steps[pipelineCtx.CurrentStep]
	logger := c.newTaskLogger(t)
	logger.LogStart(currentStep.ID, currentStep.Service)

	stepPayload := make(map[string]any)
	hasConnections := false
	for _, conn := range schema.Connections {
		if conn.ToStep == currentStep.ID {
			hasConnections = true
			var val any
			if conn.FromStep == "$initial" {
				val = t.Payload[conn.FromPort]
			} else {
				if stepOutputs, ok := pipelineCtx.Outputs[conn.FromStep]; ok {
					val = stepOutputs[conn.FromPort]
				}
			}
			stepPayload[conn.ToPort] = val
		}
	}

	if !hasConnections {
		if pipelineCtx.CurrentStep == 0 {
			for k, v := range t.Payload {
				if k != "$pipeline" && k != "requester_node_id" {
					stepPayload[k] = v
				}
			}
		} else {
			prevStepID := schema.Steps[pipelineCtx.CurrentStep-1].ID
			if prevOutputs, ok := pipelineCtx.Outputs[prevStepID]; ok {
				maps.Copy(stepPayload, prevOutputs)
			}
		}
	}

	handler, localExists := c.registry.GetHandler(currentStep.Service)
	isTargetSelf := currentStep.TargetNodeID == "" || currentStep.TargetNodeID == c.nodeID

	if localExists && isTargetSelf {
		stepPayload["requester_node_id"] = t.RequesterNodeID

		// Auto-resolve any vfs:// parameters to local physical storage paths
		if c.vfsBlobResolver != nil {
			for k, v := range stepPayload {
				if pathStr, ok := v.(string); ok {
					if hash, isVFS := protocol.ParseVFSURI(pathStr); isVFS {
						resolvedPath, err := c.vfsBlobResolver(context.Background(), t.RequesterNodeID, hash)
						if err != nil {
							logger.LogFailure(currentStep.ID, currentStep.Service, err)
							c.sendPipelineError(t, fmt.Errorf("step '%s' failed to resolve vfs://%s: %w", currentStep.ID, hash, err))
							return
						}
						if resolvedPath == "" {
							err := fmt.Errorf("empty path resolving vfs://%s", hash)
							logger.LogFailure(currentStep.ID, currentStep.Service, err)
							c.sendPipelineError(t, fmt.Errorf("step '%s' failed to resolve vfs://%s: %w", currentStep.ID, hash, err))
							return
						}
						stepPayload[k] = resolvedPath
					}
				}
			}
		}

		// Pre-validate the payload before executing the step
		if err := c.registry.ValidatePayload(currentStep.Service, stepPayload); err != nil {
			logger.LogFailure(currentStep.ID, currentStep.Service, err)
			c.sendPipelineError(t, fmt.Errorf("step '%s' failed input validation: %w", currentStep.ID, err))
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), protocol.RPCTimeoutTaskWait)
		outputs, err := handler.Execute(ctx, stepPayload)
		cancel()

		if err != nil {
			logger.LogFailure(currentStep.ID, currentStep.Service, err)
			c.sendPipelineError(t, fmt.Errorf("step '%s' failed: %w", currentStep.ID, err))
			return
		}

		// Auto-stage any generated local output file into VFS physical storage for subsequent steps
		if err := c.stageOutputBlobs(outputs); err != nil {
			logger.LogFailure(currentStep.ID, currentStep.Service, err)
			c.sendPipelineError(t, fmt.Errorf("step '%s' failed staging outputs: %w", currentStep.ID, err))
			return
		}

		pipelineCtx.Outputs[currentStep.ID] = outputs
		pipelineCtx.CurrentStep++
		t.Payload["$pipeline"] = pipelineCtx

		c.advancePipeline(t, schema)
	} else {
		c.routePipelineStep(t, currentStep, schema)
	}
}

func (c *ComputeEngine) stageOutputBlobs(outputs map[string]any) error {
	if c.vfsBlobStager == nil {
		return nil
	}
	return protocol.RewriteLocalFilePaths(outputs, c.vfsBlobStager, true)
}

func (c *ComputeEngine) routePipelineStep(t protocol.TaskRequest, step protocol.PipelineStep, schema protocol.PipelineSchema) {
	targetPeer := step.TargetNodeID
	var err error

	if targetPeer == "" || targetPeer == c.nodeID {
		_, localSupports := c.registry.GetHandler(step.Service)
		if localSupports {
			targetPeer = c.nodeID
		} else {
			if c.serviceFinder == nil {
				c.sendPipelineError(t, fmt.Errorf("service finder not configured on compute engine"))
				return
			}
			targetPeer, _, _, err = c.serviceFinder(protocol.DiscoveryQuery{
				Service: step.Service,
			})
			if err != nil {
				c.sendPipelineError(t, fmt.Errorf("failed to discover node for step '%s': %w", step.ID, err))
				return
			}
		}
	}

	if targetPeer == c.nodeID {
		err = c.SubmitTask(t)
		if err != nil {
			c.sendPipelineError(t, fmt.Errorf("failed to submit local task: %w", err))
		}
	} else {
		if c.taskDispatcher == nil {
			c.sendPipelineError(t, fmt.Errorf("task dispatcher not configured on compute engine"))
			return
		}
		err = c.taskDispatcher(targetPeer, t)
		if err != nil {
			c.sendPipelineError(t, fmt.Errorf("failed to dispatch step '%s' to peer '%s': %w", step.ID, targetPeer, err))
		}
	}
}

func (c *ComputeEngine) advancePipeline(t protocol.TaskRequest, schema protocol.PipelineSchema) {
	pipelineCtx := loadPipelineCtx(t.Payload)

	if pipelineCtx.CurrentStep < len(schema.Steps) {
		nextStep := schema.Steps[pipelineCtx.CurrentStep]
		c.routePipelineStep(t, nextStep, schema)
	} else {
		logger := c.newTaskLogger(t)

		// Return outputs of final step (clone to avoid reference cycle)
		finalOutputs := make(map[string]any)
		finalStepID := schema.Steps[len(schema.Steps)-1].ID
		if out, ok := pipelineCtx.Outputs[finalStepID]; ok {
			maps.Copy(finalOutputs, out)
		}
		finalOutputs["$pipeline_outputs"] = pipelineCtx.Outputs

		// Auto-stage any generated local output file into VFS physical storage
		if err := c.stageOutputBlobs(finalOutputs); err != nil {
			logger.LogFailure(finalStepID, schema.Steps[len(schema.Steps)-1].Service, err)
			c.sendPipelineError(t, fmt.Errorf("pipeline failed staging final outputs: %w", err))
			return
		}

		logger.LogSuccess()

		responsePayload := protocol.ServiceTaskResponse{
			TaskID:  t.TaskID,
			Service: t.Service,
			Status:  "completed",
			Outputs: finalOutputs,
		}

		c.setTaskStatus(responsePayload)

		if t.ReplyTo != "" {
			ctx, cancel := context.WithTimeout(context.Background(), protocol.RPCTimeoutTaskCallback)
			err := c.peerClient.SendTaskResponse(ctx, t.ReplyTo, responsePayload)
			cancel()
			if err != nil {
				c.logger.Error("Failed to deliver final pipeline response", "taskID", t.TaskID, "error", err)
			}
		}
	}
}

func (c *ComputeEngine) sendPipelineError(t protocol.TaskRequest, err error) {
	responsePayload := protocol.ServiceTaskResponse{
		TaskID:  t.TaskID,
		Service: t.Service,
		Status:  "failed",
		Error:   err.Error(),
	}
	c.setTaskStatus(responsePayload)
	if t.ReplyTo != "" {
		ctx, cancel := context.WithTimeout(context.Background(), protocol.RPCTimeoutTaskCallback)
		_ = c.peerClient.SendTaskResponse(ctx, t.ReplyTo, responsePayload)
		cancel()
	}
}

// EstimateTaskCost returns the same local cost estimate used for remote bids (L2).
func (ce *ComputeEngine) EstimateTaskCost(query protocol.DiscoveryQuery) (int64, bool) {
	return ce.estimateTaskCost(query)
}

// BuildServiceBid returns a full local ServiceBid including live resource scores (L2).
func (ce *ComputeEngine) BuildServiceBid(query protocol.DiscoveryQuery) (protocol.ServiceBid, bool) {
	estimated, canAccept := ce.estimateTaskCost(query)
	if !canAccept {
		return protocol.ServiceBid{CanAccept: false}, false
	}
	cpuLoad, memPressure := currentHostResourceSampler()()
	costUnits := estimated + int64(cpuLoad*200) + int64(memPressure*200)
	powerScore := int64(cpuLoad*1000) + int64(memPressure*100)
	bid := protocol.ServiceBid{
		NodeID:          ce.nodeID,
		NodeAddr:        ce.nodeAddr,
		EstimatedMillis: estimated,
		CPULoad:         cpuLoad,
		MemPressure:     memPressure,
		CostUnits:       costUnits,
		PowerScore:      powerScore,
		CanAccept:       true,
	}
	telemetry.ExportBidAsync(bid)
	return bid, true
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

func (c *ComputeEngine) MarkTaskAsFailed(req protocol.TaskRequest, reason string) {
	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:  req.TaskID,
		Service: req.Service,
		Status:  "failed",
		Outputs: map[string]any{"error": reason},
	})
}

func loadPipelineCtx(payload map[string]any) protocol.PipelineContext {
	var pipelineCtx protocol.PipelineContext
	if rawPipeline, ok := payload["$pipeline"]; ok {
		b, _ := json.Marshal(rawPipeline)
		_ = json.Unmarshal(b, &pipelineCtx)
	}
	return pipelineCtx
}

func (c *ComputeEngine) setTaskStatus(response protocol.ServiceTaskResponse) {
	c.taskStatuses.Store(response.TaskID, response)

	var keysToDelete []string
	count := 0
	c.taskStatuses.Range(func(key, value any) bool {
		count++
		if resp, ok := value.(protocol.ServiceTaskResponse); ok {
			if resp.Status == "completed" || resp.Status == "failed" {
				if taskIDStr, ok := key.(string); ok {
					keysToDelete = append(keysToDelete, taskIDStr)
				}
			}
		}
		return true
	})

	if count > 100 && len(keysToDelete) > 0 {
		toDelete := count - 100
		if toDelete > len(keysToDelete) {
			toDelete = len(keysToDelete)
		}
		for i := 0; i < toDelete; i++ {
			c.taskStatuses.Delete(keysToDelete[i])
		}
	}
}

func (c *ComputeEngine) GetAllTaskStatuses() []protocol.ServiceTaskResponse {
	list := make([]protocol.ServiceTaskResponse, 0)
	c.taskStatuses.Range(func(key, value any) bool {
		if resp, ok := value.(protocol.ServiceTaskResponse); ok {
			list = append(list, resp)
		}
		return true
	})
	return list
}

func (c *ComputeEngine) Close() {
	close(c.taskQueue)
	c.wg.Wait()
}

func (c *ComputeEngine) ClearServices() {
	c.registry.Clear()
}

func (c *ComputeEngine) isPipeline(id string) bool {
	_, exists := c.GetPipeline(id)
	return exists
}

type taskLogger interface {
	LogStart(stepID, service string)
	LogFailure(stepID, service string, err error)
	LogSuccess()
}

func (c *ComputeEngine) newTaskLogger(t protocol.TaskRequest) taskLogger {
	if c.isPipeline(t.Service) {
		return &pipelineLogger{
			logger:   c.logger,
			schemaID: t.Service,
			taskID:   t.TaskID,
		}
	}
	return &singleTaskLogger{
		logger:   c.logger,
		schemaID: t.Service,
		taskID:   t.TaskID,
	}
}

type pipelineLogger struct {
	logger   *slog.Logger
	schemaID string
	taskID   string
}

func (l *pipelineLogger) LogStart(stepID, service string) {
	l.logger.Info("Executing pipeline step", "pipeline", l.schemaID, "step", stepID, "service", service)
}

func (l *pipelineLogger) LogFailure(stepID, service string, err error) {
	l.logger.Error("Pipeline step failed", "pipelineID", l.schemaID, "stepID", stepID, "error", err)
}

func (l *pipelineLogger) LogSuccess() {
	l.logger.Info("Pipeline completed successfully", "pipelineID", l.schemaID, "taskID", l.taskID)
}

type singleTaskLogger struct {
	logger   *slog.Logger
	schemaID string
	taskID   string
}

func (l *singleTaskLogger) LogStart(stepID, service string) {
	l.logger.Info("Working on task...", "job_id", l.taskID, "service", service)
}

func (l *singleTaskLogger) LogFailure(stepID, service string, err error) {
	l.logger.Error("Task execution failed", "job_id", l.taskID, "service", service, "error", err)
}

func (l *singleTaskLogger) LogSuccess() {
	l.logger.Info("Task execution completed successfully", "job_id", l.taskID, "service", l.schemaID)
}
