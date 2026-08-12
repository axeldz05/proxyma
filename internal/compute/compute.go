package compute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/telemetry"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned when work is submitted after ComputeEngine shutdown begins.
var ErrClosed = errors.New("compute engine closed")

var (
	ErrTaskNotFound  = errors.New("task not found")
	ErrTaskTerminal  = errors.New("task already terminal")
	ErrTaskDuplicate = errors.New("duplicate task ID")
	ErrTaskProducer  = errors.New("unexpected task producer")
)

const (
	defaultTaskStatusLimit       = 100
	defaultPendingTaskStatusTTL  = 10 * time.Minute
	defaultTerminalTaskStatusTTL = 30 * time.Minute
)

type taskStatusMetadata struct {
	touchedAt          time.Time
	sequence           uint64
	expectedProducer   string
	delegatedProducers map[string]struct{}
}

type ComputeEngine struct {
	taskQueue             chan protocol.TaskRequest
	registry              *ServiceRegistry
	taskStatuses          *sync.Map
	taskStatusesMu        sync.Mutex
	taskStatusMeta        map[string]taskStatusMetadata
	retiredTaskStatuses   map[string]protocol.ServiceTaskResponse
	retiredTaskStatusMeta map[string]taskStatusMetadata
	taskStatusNow         func() time.Time
	taskStatusLimit       int
	taskStatusSeq         uint64
	pendingTaskStatusTTL  time.Duration
	terminalTaskStatusTTL time.Duration
	acceptedTasks         sync.Map
	taskSubmissionMu      sync.Mutex
	logger                *slog.Logger
	peerClient            p2p.PeerClient
	nodeID                string
	nodeAddr              string
	activeWorkers         atomic.Int32
	wg                    sync.WaitGroup
	pipelines             map[string]protocol.PipelineSchema
	pipelineRevisions     map[string]protocol.PipelineSchema
	pipelinesMu           sync.RWMutex
	serviceFinder         ServiceFinder
	taskDispatcher        TaskDispatcher
	vfsBlobResolver       VFSBlobResolver
	vfsBlobStager         VFSBlobStager
	lifetimeCtx           context.Context
	cancelLifetime        context.CancelFunc
	lifecycleMu           sync.RWMutex
	closed                bool
	closeOnce             sync.Once
}

type VFSBlobStager func(path string) (string, int64, error)
type VFSBlobResolver func(ctx context.Context, sourceNodeID, hash string) (string, error)
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

func NewComputeEngine(parent context.Context, logger *slog.Logger, pc p2p.PeerClient, workerCount int, id string) *ComputeEngine {
	if parent == nil {
		panic("compute.NewComputeEngine: nil parent context")
	}
	lifetimeCtx, cancelLifetime := context.WithCancel(parent)
	engine := &ComputeEngine{
		taskQueue:             make(chan protocol.TaskRequest, 10),
		registry:              NewServiceRegistry(),
		taskStatuses:          &sync.Map{},
		taskStatusMeta:        make(map[string]taskStatusMetadata),
		retiredTaskStatuses:   make(map[string]protocol.ServiceTaskResponse),
		retiredTaskStatusMeta: make(map[string]taskStatusMetadata),
		taskStatusNow:         time.Now,
		taskStatusLimit:       defaultTaskStatusLimit,
		pendingTaskStatusTTL:  defaultPendingTaskStatusTTL,
		terminalTaskStatusTTL: defaultTerminalTaskStatusTTL,
		logger:                logger,
		peerClient:            pc,
		nodeID:                id,
		pipelines:             make(map[string]protocol.PipelineSchema),
		pipelineRevisions:     make(map[string]protocol.PipelineSchema),
		lifetimeCtx:           lifetimeCtx,
		cancelLifetime:        cancelLifetime,
	}
	engine.wg.Add(1)
	go engine.serviceWorker(workerCount)
	go func() {
		<-lifetimeCtx.Done()
		engine.Close()
	}()

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

func (c *ComputeEngine) ValidatePipelineRegistration(schema protocol.PipelineSchema) error {
	schema = protocol.ClonePipelineSchema(schema)
	if err := protocol.ValidatePipelineSchema(schema, nil); err != nil {
		return err
	}
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	if current, exists := c.pipelineRevisions[schema.ID]; exists {
		return protocol.ValidatePipelineRevision(current, schema)
	}
	return nil
}

func (c *ComputeEngine) RegisterPipeline(schema protocol.PipelineSchema) error {
	return c.ApplyPipelineRevision(schema, nil)
}

// ApplyPipelineRevision serializes revision validation, durable staging, and memory publication.
func (c *ComputeEngine) ApplyPipelineRevision(
	schema protocol.PipelineSchema,
	persist func(protocol.PipelineSchema) error,
) error {
	schema = protocol.ClonePipelineSchema(schema)
	if err := protocol.ValidatePipelineSchema(schema, nil); err != nil {
		return err
	}

	c.pipelinesMu.Lock()
	defer c.pipelinesMu.Unlock()
	if current, exists := c.pipelineRevisions[schema.ID]; exists {
		if err := protocol.ValidatePipelineRevision(current, schema); err != nil {
			return err
		}
	}
	if persist != nil {
		if err := persist(protocol.ClonePipelineSchema(schema)); err != nil {
			return err
		}
	}

	c.pipelineRevisions[schema.ID] = protocol.ClonePipelineSchema(schema)
	if schema.Deleted {
		delete(c.pipelines, schema.ID)
		c.logger.Info("Pipeline schema tombstoned", "pipelineID", schema.ID, "version", schema.Version)
	} else {
		c.pipelines[schema.ID] = protocol.ClonePipelineSchema(schema)
		c.logger.Info("Pipeline schema registered", "pipelineID", schema.ID, "version", schema.Version)
	}
	return nil
}

func (c *ComputeEngine) UnregisterPipeline(id string) {
	c.pipelinesMu.Lock()
	defer c.pipelinesMu.Unlock()
	if current, exists := c.pipelineRevisions[id]; exists {
		current.Deleted = true
		c.pipelineRevisions[id] = protocol.ClonePipelineSchema(current)
	}
	delete(c.pipelines, id)
	c.logger.Info("Pipeline schema unregistered", "pipelineID", id)
}

func (c *ComputeEngine) GetPipeline(id string) (protocol.PipelineSchema, bool) {
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	schema, exists := c.pipelines[id]
	return protocol.ClonePipelineSchema(schema), exists
}

func (c *ComputeEngine) GetPipelineRevision(id string) (protocol.PipelineSchema, bool) {
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	schema, exists := c.pipelineRevisions[id]
	return protocol.ClonePipelineSchema(schema), exists
}

func (c *ComputeEngine) ListPipelines() []protocol.PipelineSchema {
	c.pipelinesMu.RLock()
	defer c.pipelinesMu.RUnlock()
	list := make([]protocol.PipelineSchema, 0, len(c.pipelines))
	for _, schema := range c.pipelines {
		list = append(list, protocol.ClonePipelineSchema(schema))
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

// BindPipelineTask snapshots the local pipeline revision into an outbound task.
func (c *ComputeEngine) BindPipelineTask(req *protocol.TaskRequest) error {
	if req == nil {
		return fmt.Errorf("task request is nil")
	}
	schema, exists := c.GetPipeline(req.Service)
	if !exists {
		return nil
	}
	return bindPipelineState(req, schema)
}

// PreparePipelineTargets resolves every dynamic step once at the originating
// node so dispatch and callback authorization share the same chosen peers.
func (c *ComputeEngine) PreparePipelineTargets(req *protocol.TaskRequest) error {
	if req == nil {
		return fmt.Errorf("task request is nil")
	}
	if req.PipelineState == nil {
		return nil
	}
	schema, exists := c.GetPipeline(req.Service)
	if !exists {
		return nil
	}
	if req.PipelineState.SelectedTargets == nil {
		req.PipelineState.SelectedTargets = make(map[string]string, len(schema.Steps))
	}
	for stepIndex, step := range schema.Steps {
		if req.PipelineState.SelectedTargets[step.ID] != "" {
			continue
		}
		targetPeer, err := c.selectPipelineStepTarget(step, stepIndex)
		if err != nil {
			return fmt.Errorf("select target for pipeline step '%s': %w", step.ID, err)
		}
		req.PipelineState.SelectedTargets[step.ID] = targetPeer
	}
	return nil
}

// BindPipelineStepTarget records the already chosen target for the current
// handoff, preventing preparation or routing from selecting it again.
func (c *ComputeEngine) BindPipelineStepTarget(req *protocol.TaskRequest, targetPeerID string) error {
	if req == nil || req.PipelineState == nil {
		return nil
	}
	schema, exists := c.GetPipeline(req.Service)
	if !exists {
		return nil
	}
	stepIndex := req.PipelineState.CurrentStep
	if stepIndex < 0 || stepIndex >= len(schema.Steps) {
		return fmt.Errorf("pipeline step index %d out of bounds", stepIndex)
	}
	if targetPeerID == "" {
		return fmt.Errorf("pipeline target peer is empty")
	}
	if req.PipelineState.SelectedTargets == nil {
		req.PipelineState.SelectedTargets = make(map[string]string, len(schema.Steps))
	}
	stepID := schema.Steps[stepIndex].ID
	if selected := req.PipelineState.SelectedTargets[stepID]; selected != "" && selected != targetPeerID {
		return fmt.Errorf("pipeline step '%s' target already selected as %q, cannot dispatch to %q",
			stepID, selected, targetPeerID)
	}
	req.PipelineState.SelectedTargets[stepID] = targetPeerID
	return nil
}

func (c *ComputeEngine) selectPipelineStepTarget(step protocol.PipelineStep, stepIndex int) (string, error) {
	targetPeer := step.TargetNodeID
	if targetPeer != "" && targetPeer != c.nodeID {
		return targetPeer, nil
	}
	if _, localSupports := c.registry.GetHandler(step.Service); localSupports {
		return c.nodeID, nil
	}
	if c.serviceFinder == nil {
		return "", fmt.Errorf("service finder not configured on compute engine")
	}
	query := protocol.DiscoveryQuery{Service: step.Service}
	if stepIndex > 0 {
		query.RequiredCapabilities = map[string]int{
			protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
		}
	}
	targetPeer, _, _, err := c.serviceFinder(query)
	if err != nil {
		return "", err
	}
	if targetPeer == "" {
		return "", fmt.Errorf("service finder returned an empty peer")
	}
	return targetPeer, nil
}

func (c *ComputeEngine) GetTaskResponse(taskID string) (protocol.ServiceTaskResponse, bool) {
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	now := c.taskStatusNow()
	c.pruneTaskStatusesLocked(now, taskID)
	val, exists := c.taskStatuses.Load(taskID)
	if !exists {
		response, retired := c.retiredTaskStatuses[taskID]
		if !retired {
			return protocol.ServiceTaskResponse{}, false
		}
		c.touchRetiredTaskStatusLocked(taskID, now)
		return protocol.CloneServiceTaskResponse(response), true
	}
	res, ok := val.(protocol.ServiceTaskResponse)
	if !ok {
		return protocol.ServiceTaskResponse{}, false
	}
	c.touchTaskStatusLocked(taskID, now)
	return protocol.CloneServiceTaskResponse(res), true
}

func (c *ComputeEngine) serviceWorker(maxWorkers int) {
	defer c.wg.Done()
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	sem := make(chan struct{}, maxWorkers)
	for {
		if c.lifetimeCtx.Err() != nil {
			return
		}
		var task protocol.TaskRequest
		select {
		case <-c.lifetimeCtx.Done():
			return
		case task = <-c.taskQueue:
		}
		select {
		case <-c.lifetimeCtx.Done():
			return
		case sem <- struct{}{}:
		}
		if c.lifetimeCtx.Err() != nil {
			<-sem
			return
		}
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
	} else {
		t.Payload = maps.Clone(t.Payload)
	}
	delete(t.Payload, "$pipeline")

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

	if err := bindPipelineState(&t, pipelineSchema); err != nil {
		c.sendPipelineError(t, err)
		return
	}
	c.executePipelineStep(t, pipelineSchema)
}

func bindPipelineState(t *protocol.TaskRequest, schema protocol.PipelineSchema) error {
	schemaHash := protocol.PipelineSchemaHash(schema)
	if t.PipelineState == nil {
		t.PipelineState = &protocol.PipelineExecutionState{
			PipelineID:      schema.ID,
			PipelineVersion: schema.Version,
			SchemaHash:      schemaHash,
			Outputs:         make(map[string]map[string]any),
			OutputProducers: map[string]string{"$initial": t.RequesterNodeID},
		}
		return nil
	}

	incoming := t.PipelineState
	if incoming.PipelineID != schema.ID ||
		incoming.PipelineVersion != schema.Version ||
		incoming.SchemaHash != schemaHash {
		return fmt.Errorf(
			"pipeline schema mismatch for '%s': task is bound to version %d hash %s, local schema is version %d hash %s",
			schema.ID, incoming.PipelineVersion, incoming.SchemaHash, schema.Version, schemaHash,
		)
	}
	if incoming.CurrentStep > len(schema.Steps) {
		return fmt.Errorf("step index %d out of bounds", incoming.CurrentStep)
	}
	if incoming.CurrentStep > 0 {
		previousStepID := schema.Steps[incoming.CurrentStep-1].ID
		if incoming.OutputProducers[previousStepID] == "" {
			return fmt.Errorf("pipeline provenance missing for completed step '%s'", previousStepID)
		}
	}
	t.PipelineState = protocol.ClonePipelineExecutionState(incoming)
	if t.PipelineState.OutputProducers == nil {
		t.PipelineState.OutputProducers = make(map[string]string)
	}
	if t.PipelineState.OutputProducers["$initial"] == "" {
		t.PipelineState.OutputProducers["$initial"] = t.RequesterNodeID
	}
	return nil
}

func (c *ComputeEngine) executePipelineStep(t protocol.TaskRequest, schema protocol.PipelineSchema) {
	pipelineState := t.PipelineState
	if pipelineState == nil {
		c.sendPipelineError(t, fmt.Errorf("pipeline execution state is missing"))
		return
	}
	if pipelineState.Outputs == nil {
		pipelineState.Outputs = make(map[string]map[string]any)
	}

	if pipelineState.CurrentStep < 0 || pipelineState.CurrentStep >= len(schema.Steps) {
		c.logger.Error("Pipeline step index out of bounds", "step", pipelineState.CurrentStep, "total", len(schema.Steps))
		c.sendPipelineError(t, fmt.Errorf("step index %d out of bounds", pipelineState.CurrentStep))
		return
	}

	currentStep := schema.Steps[pipelineState.CurrentStep]
	logger := c.newTaskLogger(t)
	logger.LogStart(currentStep.ID, currentStep.Service)

	stepPayload := make(map[string]any)
	inputProducers := make(map[string]string)
	hasConnections := false
	for _, conn := range schema.Connections {
		if conn.ToStep == currentStep.ID {
			hasConnections = true
			var val any
			if conn.FromStep == "$initial" {
				val = t.Payload[conn.FromPort]
				inputProducers[conn.ToPort] = pipelineState.OutputProducers["$initial"]
			} else {
				if stepOutputs, ok := pipelineState.Outputs[conn.FromStep]; ok {
					val = stepOutputs[conn.FromPort]
				}
				inputProducers[conn.ToPort] = pipelineState.OutputProducers[conn.FromStep]
			}
			stepPayload[conn.ToPort] = val
		}
	}

	if !hasConnections {
		if pipelineState.CurrentStep == 0 {
			for k, v := range t.Payload {
				if k != "$pipeline" && k != "requester_node_id" {
					stepPayload[k] = v
					inputProducers[k] = pipelineState.OutputProducers["$initial"]
				}
			}
		} else {
			prevStepID := schema.Steps[pipelineState.CurrentStep-1].ID
			if prevOutputs, ok := pipelineState.Outputs[prevStepID]; ok {
				maps.Copy(stepPayload, prevOutputs)
				for key := range prevOutputs {
					inputProducers[key] = pipelineState.OutputProducers[prevStepID]
				}
			}
		}
	}

	handler, localExists := c.registry.GetHandler(currentStep.Service)
	selectedTarget := pipelineState.SelectedTargets[currentStep.ID]
	isTargetSelf := selectedTarget == c.nodeID ||
		(selectedTarget == "" && (currentStep.TargetNodeID == "" || currentStep.TargetNodeID == c.nodeID))

	if localExists && isTargetSelf {
		stepPayload["requester_node_id"] = t.RequesterNodeID

		// Auto-resolve any vfs:// parameters to local physical storage paths
		if c.vfsBlobResolver != nil {
			for k, v := range stepPayload {
				if pathStr, ok := v.(string); ok {
					if hash, isVFS := protocol.ParseVFSURI(pathStr); isVFS {
						sourceNodeID := inputProducers[k]
						if sourceNodeID == "" {
							sourceNodeID = t.RequesterNodeID
						}
						resolvedPath, err := c.vfsBlobResolver(c.lifetimeCtx, sourceNodeID, hash)
						if err != nil {
							logger.LogFailure(currentStep.ID, currentStep.Service, err)
							c.sendPipelineError(t, fmt.Errorf("step '%s' failed to resolve vfs://%s from '%s': %w", currentStep.ID, hash, sourceNodeID, err))
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

		ctx, cancel := context.WithTimeout(c.lifetimeCtx, protocol.RPCTimeoutTaskWait)
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

		pipelineState.Outputs[currentStep.ID] = outputs
		pipelineState.OutputProducers[currentStep.ID] = c.nodeID
		pipelineState.CurrentStep++

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
	stepIndex := 0
	targetPeer := ""
	if t.PipelineState != nil {
		stepIndex = t.PipelineState.CurrentStep
		targetPeer = t.PipelineState.SelectedTargets[step.ID]
	}
	if targetPeer == "" {
		var err error
		targetPeer, err = c.selectPipelineStepTarget(step, stepIndex)
		if err != nil {
			c.sendPipelineError(t, fmt.Errorf("failed to discover node for step '%s': %w", step.ID, err))
			return
		}
		if t.PipelineState != nil {
			if t.PipelineState.SelectedTargets == nil {
				t.PipelineState.SelectedTargets = make(map[string]string, len(schema.Steps))
			}
			t.PipelineState.SelectedTargets[step.ID] = targetPeer
		}
	}

	var err error
	if targetPeer == c.nodeID {
		c.acceptedTasks.Delete(t.TaskID)
		t.ExpectedProducerNodeID = c.nodeID
		err = c.SubmitTask(t)
		if err != nil {
			c.sendPipelineError(t, fmt.Errorf("failed to submit local task: %w", err))
		}
	} else {
		if c.taskDispatcher == nil {
			c.sendPipelineError(t, fmt.Errorf("task dispatcher not configured on compute engine"))
			return
		}
		t.ExpectedProducerNodeID = targetPeer
		err = c.taskDispatcher(targetPeer, t)
		if err != nil {
			c.sendPipelineError(t, fmt.Errorf("failed to dispatch step '%s' to peer '%s': %w", step.ID, targetPeer, err))
			return
		}
		c.acceptedTasks.Delete(t.TaskID)
	}
}

func (c *ComputeEngine) advancePipeline(t protocol.TaskRequest, schema protocol.PipelineSchema) {
	pipelineState := t.PipelineState
	if pipelineState == nil {
		c.sendPipelineError(t, fmt.Errorf("pipeline execution state is missing"))
		return
	}

	if pipelineState.CurrentStep < len(schema.Steps) {
		nextStep := schema.Steps[pipelineState.CurrentStep]
		c.routePipelineStep(t, nextStep, schema)
	} else {
		logger := c.newTaskLogger(t)

		// Return outputs of final step (clone to avoid reference cycle)
		finalOutputs := make(map[string]any)
		finalStepID := schema.Steps[len(schema.Steps)-1].ID
		if out, ok := pipelineState.Outputs[finalStepID]; ok {
			finalOutputs = protocol.CloneJSONMap(out)
		}
		finalOutputs["$pipeline_outputs"] = protocol.ClonePipelineExecutionState(pipelineState).Outputs

		// Auto-stage any generated local output file into VFS physical storage
		if err := c.stageOutputBlobs(finalOutputs); err != nil {
			logger.LogFailure(finalStepID, schema.Steps[len(schema.Steps)-1].Service, err)
			c.sendPipelineError(t, fmt.Errorf("pipeline failed staging final outputs: %w", err))
			return
		}

		logger.LogSuccess()

		responsePayload := protocol.ServiceTaskResponse{
			TaskID:         t.TaskID,
			Service:        t.Service,
			Status:         "completed",
			Outputs:        finalOutputs,
			ProducerNodeID: c.nodeID,
		}

		storedResponse := responsePayload
		if t.ReplyTo != "" && t.RequesterNodeID == c.nodeID && taskNeedsOutputIngest(responsePayload) {
			storedResponse.Status = protocol.TaskStatusIngesting
		}
		c.setTaskStatus(storedResponse)

		if t.ReplyTo != "" {
			ctx, cancel := context.WithTimeout(c.lifetimeCtx, protocol.RPCTimeoutTaskCallback)
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
		TaskID:         t.TaskID,
		Service:        t.Service,
		Status:         "failed",
		Error:          err.Error(),
		ProducerNodeID: c.nodeID,
	}
	c.setTaskStatus(responsePayload)
	if t.ReplyTo != "" {
		ctx, cancel := context.WithTimeout(c.lifetimeCtx, protocol.RPCTimeoutTaskCallback)
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
	capabilities := map[string]int{
		protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
	}
	if !protocol.SupportsCapabilities(capabilities, query.RequiredCapabilities) {
		return protocol.ServiceBid{CanAccept: false, Capabilities: capabilities}, false
	}
	estimated, canAccept := ce.estimateTaskCost(query)
	if !canAccept {
		return protocol.ServiceBid{CanAccept: false, Capabilities: capabilities}, false
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
		Capabilities:    capabilities,
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
	if req.TaskID == "" {
		return fmt.Errorf("%w: task ID is empty", ErrTaskDuplicate)
	}
	if req.RequesterNodeID == "" {
		req.RequesterNodeID = c.nodeID
	}
	if req.PipelineState == nil {
		if err := c.BindPipelineTask(&req); err != nil {
			return err
		}
	}
	req = protocol.CloneTaskRequest(req)

	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	if c.closed || c.lifetimeCtx.Err() != nil {
		return ErrClosed
	}

	c.taskSubmissionMu.Lock()
	defer c.taskSubmissionMu.Unlock()
	if status, exists := c.taskStatuses.Load(req.TaskID); exists {
		if response, ok := status.(protocol.ServiceTaskResponse); ok &&
			isTerminalTaskStatus(response.Status) {
			if response.Service == req.Service {
				return nil
			}
			return fmt.Errorf("%w %q: existing service %q, new service %q",
				ErrTaskDuplicate, req.TaskID, response.Service, req.Service)
		}
	}
	c.taskStatusesMu.Lock()
	retired, wasRetired := c.retiredTaskStatuses[req.TaskID]
	c.taskStatusesMu.Unlock()
	if wasRetired {
		if retired.Service == req.Service {
			return nil
		}
		return fmt.Errorf("%w %q: retired service %q, new service %q",
			ErrTaskDuplicate, req.TaskID, retired.Service, req.Service)
	}
	if previous, loaded := c.acceptedTasks.Load(req.TaskID); loaded {
		if existing, ok := previous.(protocol.TaskRequest); ok && reflect.DeepEqual(existing, req) {
			return nil
		}
		return fmt.Errorf("%w %q", ErrTaskDuplicate, req.TaskID)
	}
	c.acceptedTasks.Store(req.TaskID, req)
	select {
	case c.taskQueue <- req:
		c.logger.Debug("Task accepted into queue", "taskID", req.TaskID)
		return nil
	default:
		c.acceptedTasks.Delete(req.TaskID)
		return fmt.Errorf("node is overloaded: task queue is full")
	}
}

func (c *ComputeEngine) RegisterOutgoingTask(req protocol.TaskRequest) {
	response := protocol.ServiceTaskResponse{
		TaskID:  req.TaskID,
		Service: req.Service,
		Status:  "pending",
	}
	delegatedProducers := make(map[string]struct{})
	if req.PipelineState != nil {
		if schema, exists := c.GetPipeline(req.Service); exists {
			for _, step := range schema.Steps {
				targetNodeID := req.PipelineState.SelectedTargets[step.ID]
				if targetNodeID == "" {
					targetNodeID = step.TargetNodeID
				}
				if targetNodeID != "" {
					delegatedProducers[targetNodeID] = struct{}{}
				}
			}
		}
	}
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	if value, exists := c.taskStatuses.Load(req.TaskID); exists {
		if current, ok := value.(protocol.ServiceTaskResponse); ok {
			if current.Service == req.Service {
				return
			}
			return
		}
	}
	c.taskStatuses.Store(req.TaskID, response)
	now := c.taskStatusNow()
	c.touchTaskStatusLocked(req.TaskID, now)
	metadata := c.taskStatusMeta[req.TaskID]
	metadata.expectedProducer = req.ExpectedProducerNodeID
	metadata.delegatedProducers = delegatedProducers
	c.taskStatusMeta[req.TaskID] = metadata
	c.pruneTaskStatusesLocked(now, req.TaskID)
}

func (c *ComputeEngine) MarkTaskAsFailed(req protocol.TaskRequest, reason string) {
	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:  req.TaskID,
		Service: req.Service,
		Status:  "failed",
		Error:   reason,
		Outputs: map[string]any{"error": reason},
	})
}

// AcceptTaskCallback authenticates and atomically records a callback for a tracked task.
// Trust model: enrolled mTLS peers and explicitly bound pipeline delegates are trusted
// producers. Producer/delegation fields are authorization metadata, not cryptographic
// provenance proofs.
func (c *ComputeEngine) AcceptTaskCallback(producerNodeID string, response protocol.ServiceTaskResponse) error {
	response = protocol.CloneServiceTaskResponse(response)
	if producerNodeID == "" {
		return fmt.Errorf("authenticated producer is empty")
	}
	if response.ProducerNodeID != "" && response.ProducerNodeID != producerNodeID {
		return fmt.Errorf("producer node ID %q does not match authenticated peer %q", response.ProducerNodeID, producerNodeID)
	}

	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	value, exists := c.taskStatuses.Load(response.TaskID)
	if !exists {
		if _, retired := c.retiredTaskStatuses[response.TaskID]; retired {
			return ErrTaskTerminal
		}
		return ErrTaskNotFound
	}
	current, ok := value.(protocol.ServiceTaskResponse)
	if !ok {
		return ErrTaskNotFound
	}
	expectedProducer := c.taskStatusMeta[response.TaskID].expectedProducer
	if expectedProducer == "" {
		return fmt.Errorf("%w: no producer was registered for task %q", ErrTaskProducer, response.TaskID)
	}
	_, delegated := c.taskStatusMeta[response.TaskID].delegatedProducers[producerNodeID]
	if expectedProducer != producerNodeID && !delegated {
		return fmt.Errorf("%w: expected %q, got %q", ErrTaskProducer, expectedProducer, producerNodeID)
	}
	if response.Service == "" {
		response.Service = current.Service
	} else if current.Service != "" && response.Service != current.Service {
		return fmt.Errorf("task service mismatch: expected %q, got %q", current.Service, response.Service)
	}
	if isTerminalTaskStatus(current.Status) {
		if current.Status == response.Status &&
			(current.ProducerNodeID == "" || current.ProducerNodeID == producerNodeID) {
			return nil
		}
		return ErrTaskTerminal
	}
	if current.Status == protocol.TaskStatusIngesting {
		if response.Status == "completed" {
			return nil
		}
		return ErrTaskTerminal
	}

	response.ProducerNodeID = producerNodeID
	switch response.Status {
	case "completed":
		if taskNeedsOutputIngest(response) {
			response.Status = protocol.TaskStatusIngesting
		} else {
			c.acceptedTasks.Delete(response.TaskID)
		}
	case "failed":
		c.acceptedTasks.Delete(response.TaskID)
	default:
		return fmt.Errorf("invalid callback task status %q", response.Status)
	}
	c.storeTaskStatusLocked(response)
	return nil
}

// RecordTaskResponse publishes the trusted result of server-side output ingest.
func (c *ComputeEngine) RecordTaskResponse(response protocol.ServiceTaskResponse) {
	response = protocol.CloneServiceTaskResponse(response)
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	if _, retired := c.retiredTaskStatuses[response.TaskID]; retired {
		return
	}
	if value, exists := c.taskStatuses.Load(response.TaskID); exists {
		if current, ok := value.(protocol.ServiceTaskResponse); ok && current.Status == "failed" {
			return
		}
	}
	if isTerminalTaskStatus(response.Status) {
		c.acceptedTasks.Delete(response.TaskID)
	}
	c.storeTaskStatusLocked(response)
}

func (c *ComputeEngine) setTaskStatus(response protocol.ServiceTaskResponse) {
	response = protocol.CloneServiceTaskResponse(response)
	c.lifecycleMu.RLock()
	closed := c.closed
	c.lifecycleMu.RUnlock()
	if closed {
		response.Status = "failed"
		response.Error = ErrClosed.Error()
		response.Outputs = map[string]any{"error": ErrClosed.Error()}
	}

	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	if _, retired := c.retiredTaskStatuses[response.TaskID]; retired {
		return
	}
	if value, exists := c.taskStatuses.Load(response.TaskID); exists {
		if current, ok := value.(protocol.ServiceTaskResponse); ok && isTerminalTaskStatus(current.Status) {
			return
		}
	}
	if isTerminalTaskStatus(response.Status) {
		c.acceptedTasks.Delete(response.TaskID)
	}
	c.storeTaskStatusLocked(response)
}

func isTerminalTaskStatus(status string) bool {
	return status == "completed" || status == "failed"
}

func taskNeedsOutputIngest(response protocol.ServiceTaskResponse) bool {
	hash, _, _ := protocol.OutputHashFromOutputs(response.Outputs)
	return hash != ""
}

func (c *ComputeEngine) storeTaskStatusLocked(response protocol.ServiceTaskResponse) {
	c.taskStatuses.Store(response.TaskID, protocol.CloneServiceTaskResponse(response))
	now := c.taskStatusNow()
	c.touchTaskStatusLocked(response.TaskID, now)
	c.pruneTaskStatusesLocked(now, response.TaskID)
}

func (c *ComputeEngine) touchTaskStatusLocked(taskID string, now time.Time) {
	c.taskStatusSeq++
	metadata := c.taskStatusMeta[taskID]
	metadata.touchedAt = now
	metadata.sequence = c.taskStatusSeq
	c.taskStatusMeta[taskID] = metadata
}

type taskStatusEvictionCandidate struct {
	taskID   string
	terminal bool
	touched  time.Time
	sequence uint64
}

func (c *ComputeEngine) pruneTaskStatusesLocked(now time.Time, protectedTaskID string) {
	c.pruneRetiredTaskStatusesLocked(now, protectedTaskID)
	candidates := make([]taskStatusEvictionCandidate, 0)
	c.taskStatuses.Range(func(key, value any) bool {
		taskID, keyOK := key.(string)
		response, responseOK := value.(protocol.ServiceTaskResponse)
		if !keyOK || !responseOK {
			c.taskStatuses.Delete(key)
			return true
		}
		metadata, ok := c.taskStatusMeta[taskID]
		if !ok {
			c.touchTaskStatusLocked(taskID, now)
			metadata = c.taskStatusMeta[taskID]
		}
		terminal := isTerminalTaskStatus(response.Status)
		ttl := c.pendingTaskStatusTTL
		if terminal {
			ttl = c.terminalTaskStatusTTL
		}
		if ttl > 0 && now.Sub(metadata.touchedAt) >= ttl {
			if terminal {
				c.deleteTaskStatusLocked(taskID)
			} else {
				c.retireActiveTaskStatusLocked(
					taskID,
					response,
					now,
					"task expired before reaching a terminal callback",
				)
			}
			return true
		}
		candidates = append(candidates, taskStatusEvictionCandidate{
			taskID:   taskID,
			terminal: terminal,
			touched:  metadata.touchedAt,
			sequence: metadata.sequence,
		})
		return true
	})

	if c.taskStatusLimit <= 0 || len(candidates) <= c.taskStatusLimit {
		c.pruneRetiredTaskStatusesLocked(now, protectedTaskID)
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].taskID == protectedTaskID {
			return false
		}
		if candidates[j].taskID == protectedTaskID {
			return true
		}
		if !candidates[i].touched.Equal(candidates[j].touched) {
			return candidates[i].touched.Before(candidates[j].touched)
		}
		if candidates[i].terminal != candidates[j].terminal {
			return candidates[i].terminal
		}
		return candidates[i].sequence < candidates[j].sequence
	})
	toDelete := len(candidates) - c.taskStatusLimit
	for _, candidate := range candidates {
		if toDelete == 0 {
			break
		}
		if candidate.taskID == protectedTaskID {
			continue
		}
		value, exists := c.taskStatuses.Load(candidate.taskID)
		if !exists {
			continue
		}
		response, ok := value.(protocol.ServiceTaskResponse)
		if !ok || candidate.terminal {
			c.deleteTaskStatusLocked(candidate.taskID)
		} else {
			c.retireActiveTaskStatusLocked(
				candidate.taskID,
				response,
				now,
				"task failed because status capacity was exhausted",
			)
		}
		toDelete--
	}
	c.pruneRetiredTaskStatusesLocked(now, protectedTaskID)
}

func (c *ComputeEngine) deleteTaskStatusLocked(taskID string) {
	c.taskStatuses.Delete(taskID)
	delete(c.taskStatusMeta, taskID)
}

func (c *ComputeEngine) retireActiveTaskStatusLocked(
	taskID string,
	response protocol.ServiceTaskResponse,
	now time.Time,
	reason string,
) {
	metadata := c.taskStatusMeta[taskID]
	c.deleteTaskStatusLocked(taskID)
	response.Status = "failed"
	response.Error = reason
	response.Outputs = map[string]any{"error": reason}
	c.retiredTaskStatuses[taskID] = protocol.CloneServiceTaskResponse(response)
	c.taskStatusSeq++
	metadata.touchedAt = now
	metadata.sequence = c.taskStatusSeq
	c.retiredTaskStatusMeta[taskID] = metadata
	c.acceptedTasks.Delete(taskID)
}

func (c *ComputeEngine) touchRetiredTaskStatusLocked(taskID string, now time.Time) {
	c.taskStatusSeq++
	metadata := c.retiredTaskStatusMeta[taskID]
	metadata.touchedAt = now
	metadata.sequence = c.taskStatusSeq
	c.retiredTaskStatusMeta[taskID] = metadata
}

func (c *ComputeEngine) pruneRetiredTaskStatusesLocked(now time.Time, protectedTaskID string) {
	candidates := make([]taskStatusEvictionCandidate, 0, len(c.retiredTaskStatuses))
	for taskID := range c.retiredTaskStatuses {
		metadata := c.retiredTaskStatusMeta[taskID]
		if c.terminalTaskStatusTTL > 0 &&
			now.Sub(metadata.touchedAt) >= c.terminalTaskStatusTTL &&
			taskID != protectedTaskID {
			delete(c.retiredTaskStatuses, taskID)
			delete(c.retiredTaskStatusMeta, taskID)
			continue
		}
		candidates = append(candidates, taskStatusEvictionCandidate{
			taskID:   taskID,
			terminal: true,
			touched:  metadata.touchedAt,
			sequence: metadata.sequence,
		})
	}
	if c.taskStatusLimit <= 0 || len(candidates) <= c.taskStatusLimit {
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].taskID == protectedTaskID {
			return false
		}
		if candidates[j].taskID == protectedTaskID {
			return true
		}
		if !candidates[i].touched.Equal(candidates[j].touched) {
			return candidates[i].touched.Before(candidates[j].touched)
		}
		return candidates[i].sequence < candidates[j].sequence
	})
	toDelete := len(candidates) - c.taskStatusLimit
	for _, candidate := range candidates {
		if toDelete == 0 {
			break
		}
		if candidate.taskID == protectedTaskID {
			continue
		}
		delete(c.retiredTaskStatuses, candidate.taskID)
		delete(c.retiredTaskStatusMeta, candidate.taskID)
		toDelete--
	}
}

func (c *ComputeEngine) failAcceptedTasks() {
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	c.acceptedTasks.Range(func(key, value any) bool {
		req, ok := value.(protocol.TaskRequest)
		if ok {
			if existing, exists := c.taskStatuses.Load(req.TaskID); exists {
				if response, ok := existing.(protocol.ServiceTaskResponse); ok && isTerminalTaskStatus(response.Status) {
					c.acceptedTasks.Delete(key)
					return true
				}
			}
			c.storeTaskStatusLocked(protocol.ServiceTaskResponse{
				TaskID:  req.TaskID,
				Service: req.Service,
				Status:  "failed",
				Error:   ErrClosed.Error(),
				Outputs: map[string]any{"error": ErrClosed.Error()},
			})
			c.acceptedTasks.Delete(key)
		}
		return true
	})
}

func (c *ComputeEngine) GetAllTaskStatuses() []protocol.ServiceTaskResponse {
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	now := c.taskStatusNow()
	c.pruneTaskStatusesLocked(now, "")
	list := make([]protocol.ServiceTaskResponse, 0)
	c.taskStatuses.Range(func(key, value any) bool {
		if resp, ok := value.(protocol.ServiceTaskResponse); ok {
			list = append(list, protocol.CloneServiceTaskResponse(resp))
		}
		return true
	})
	for _, response := range c.retiredTaskStatuses {
		list = append(list, protocol.CloneServiceTaskResponse(response))
	}
	return list
}

func (c *ComputeEngine) Close() {
	c.closeOnce.Do(func() {
		c.lifecycleMu.Lock()
		c.closed = true
		c.failAcceptedTasks()
		c.cancelLifetime()
		c.lifecycleMu.Unlock()
	})
	c.wg.Wait()
	c.failAcceptedTasks()
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
