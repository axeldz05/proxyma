package compute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/telemetry"
	"proxyma/internal/utils"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// TaskJournal defines the storage WAL interface for recording task checkpoints and intermediate outputs.
type TaskJournal interface {
	AppendTaskCheckpoint(record storage.TaskJournalRecord) error
	SaveIntermediateOutput(taskID string, stepID string, outputs map[string]any) error
	PruneIntermediateTaskData(taskID string) error
}

func (c *ComputeEngine) recordCheckpoint(record storage.TaskJournalRecord) error {
	if c.taskJournal == nil {
		return nil
	}
	return c.taskJournal.AppendTaskCheckpoint(record)
}

func (c *ComputeEngine) checkpointFromTask(t protocol.TaskRequest, status string) storage.TaskJournalRecord {
	return storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:          t.TaskID,
			Service:         t.Service,
			RequesterNodeID: t.RequesterNodeID,
			ReplyTo:         t.ReplyTo,
			Status:          status,
			NodeID:          c.nodeID,
			Payload:         protocol.CloneJSONMap(t.Payload),
			PipelineState:   protocol.ClonePipelineExecutionState(t.PipelineState),
		},
		Timestamp: time.Now().UTC(),
	}
}

func (c *ComputeEngine) recordTerminalResponseCheckpoint(response protocol.ServiceTaskResponse, producerNodeID string) {
	if !isTerminalTaskStatus(response.Status) {
		return
	}
	if producerNodeID == "" {
		producerNodeID = response.ProducerNodeID
	}
	_ = c.recordCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:  response.TaskID,
			Service: response.Service,
			Status:  response.Status,
			Error:   response.Error,
			NodeID:  producerNodeID,
		},
		Outputs:   protocol.CloneJSONMap(response.Outputs),
		Timestamp: time.Now().UTC(),
	})
}

// ErrClosed is returned when work is submitted after ComputeEngine shutdown begins.
var ErrClosed = errors.New("compute engine closed")

var (
	ErrTaskNotFound  = errors.New("task not found")
	ErrTaskTerminal  = errors.New("task already terminal")
	ErrTaskDuplicate = errors.New("duplicate task ID")
	ErrTaskProducer  = errors.New("unexpected task producer")
	ErrTaskEscrowed  = errors.New("task escalated to escrow")
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
	taskJournal           TaskJournal
	vfsBlobResolver       VFSBlobResolver
	vfsBlobStager         VFSBlobStager
	escrowTasksMu         sync.RWMutex
	escrowTasks           map[string]protocol.TaskEscrowRequest
	taskEscrowDispatcher  TaskEscrowDispatcher
	escrowSupervisorOnce  sync.Once
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
type TaskEscrowDispatcher func(coordinatorPeerID string, req protocol.TaskEscrowRequest) error

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

func NewComputeEngine(parent context.Context, logger *slog.Logger, pc p2p.PeerClient, workerCount int, id string) (*ComputeEngine, error) {
	if parent == nil {
		return nil, errors.New("compute.NewComputeEngine: nil parent context")
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
		escrowTasks:           make(map[string]protocol.TaskEscrowRequest),
		lifetimeCtx:           lifetimeCtx,
		cancelLifetime:        cancelLifetime,
	}
	engine.wg.Add(1)
	go engine.serviceWorker(workerCount)
	go func() {
		<-lifetimeCtx.Done()
		engine.Close()
	}()

	return engine, nil
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

func (c *ComputeEngine) SetTaskEscrowDispatcher(dispatcher TaskEscrowDispatcher) {
	c.taskEscrowDispatcher = dispatcher
}

func (c *ComputeEngine) SetTaskJournal(journal TaskJournal) {
	c.taskJournal = journal
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
	if !exists && req.PipelineState != nil && req.PipelineState.Snapshot != nil {
		schema = *req.PipelineState.Snapshot
		exists = true
	}
	if !exists {
		return nil
	}
	_, err := c.bindPipelineState(req, schema)
	return err
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
	if !exists && req.PipelineState.Snapshot != nil {
		schema = *req.PipelineState.Snapshot
		exists = true
	}
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
			if stepIndex > 0 {
				continue
			}
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
	if !exists && req.PipelineState.Snapshot != nil {
		schema = *req.PipelineState.Snapshot
		exists = true
	}
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
		// Wait for a task
		select {
		case <-c.lifetimeCtx.Done():
			return
		case task = <-c.taskQueue:
		}
		// Semaphore permit
		select {
		case <-c.lifetimeCtx.Done():
			return
		case sem <- struct{}{}: // blocks until activeWorkers < maxWorkers
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

// processTask executes or advances a singular task or a multi-step pipeline.
// Dynamic schema evolution is supported: if a pipeline definition is updated while
// in-flight, prefix-preserving revisions are validated and adopted via tryDynamicSchemaMigration,
// while breaking changes safely fall back to the pipeline snapshot pinned at task start.
// Distributed state durability and crash recovery are backed by the Task Journal WAL
// (bucketTasksJournal) and coordinator escrow supervisor failover (/services/escrow).
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
	if !isPipeline && t.PipelineState != nil && t.PipelineState.Snapshot != nil {
		pipelineSchema = *t.PipelineState.Snapshot
		isPipeline = true
	}
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

	execSchema, err := c.bindPipelineState(&t, pipelineSchema)
	if err != nil {
		c.sendPipelineError(t, err)
		return
	}
	c.executePipelineStep(t, execSchema)
}

// tryDynamicSchemaMigration attempts to migrate an in-flight pipeline state to targetSchema
// if targetSchema is a newer revision and satisfies prefix preservation invariants.
// Returns the migrated schema if successful, or currentSchema if migration is rejected or unnecessary.
func (c *ComputeEngine) tryDynamicSchemaMigration(t protocol.TaskRequest, state *protocol.PipelineExecutionState, currentSchema, targetSchema protocol.PipelineSchema) protocol.PipelineSchema {
	if state == nil || currentSchema.ID != targetSchema.ID || len(targetSchema.Steps) == 0 {
		return currentSchema
	}

	targetHash := protocol.PipelineSchemaHash(targetSchema)
	isUpdated := targetSchema.Version > state.PipelineVersion ||
		(targetSchema.Version == state.PipelineVersion && targetHash != state.SchemaHash)
	if !isUpdated {
		return currentSchema
	}

	completedSteps := make([]string, 0, state.CurrentStep)
	for i := 0; i < state.CurrentStep && i < len(currentSchema.Steps); i++ {
		completedSteps = append(completedSteps, currentSchema.Steps[i].ID)
	}

	migrationErr := protocol.ValidatePrefixMigration(completedSteps, state.Outputs, currentSchema, targetSchema)
	if migrationErr != nil {
		c.logger.Info("Dynamic schema migration rejected, falling back to snapshot",
			"pipelineID", state.PipelineID,
			"reason", migrationErr,
		)
		return currentSchema
	}

	c.logger.Info("Dynamic schema migration accepted for pipeline",
		"pipelineID", state.PipelineID,
		"fromVersion", state.PipelineVersion,
		"toVersion", targetSchema.Version,
	)
	fromVersion := state.PipelineVersion
	state.PipelineVersion = targetSchema.Version
	state.SchemaHash = targetHash
	cloned := protocol.ClonePipelineSchema(targetSchema)
	state.Snapshot = &cloned

	// Resolve candidates for upcoming migrated steps and track in delegatedProducers
	if state.SelectedTargets == nil {
		state.SelectedTargets = make(map[string]string, len(targetSchema.Steps))
	}
	for i := state.CurrentStep; i < len(targetSchema.Steps); i++ {
		step := targetSchema.Steps[i]
		if state.SelectedTargets[step.ID] == "" {
			if step.TargetNodeID != "" {
				state.SelectedTargets[step.ID] = step.TargetNodeID
			} else if cand, err := c.selectPipelineStepTarget(step, i); err == nil && cand != "" {
				state.SelectedTargets[step.ID] = cand
			}
		}
	}
	c.taskStatusesMu.Lock()
	meta := c.taskStatusMeta[t.TaskID]
	if meta.delegatedProducers == nil {
		meta.delegatedProducers = make(map[string]struct{})
	}
	protocol.PopulateMapIf(meta.delegatedProducers, slices.Collect(maps.Values(state.SelectedTargets)), struct{}{}, func(target string) bool {
		return target != ""
	})
	c.taskStatusMeta[t.TaskID] = meta
	c.taskStatusesMu.Unlock()

	rec := c.checkpointFromTask(t, protocol.TaskEventSchemaMigrated)
	rec.StepIndex = state.CurrentStep
	rec.PipelineState = protocol.ClonePipelineExecutionState(state)
	rec.Metadata = map[string]string{
		"from_version": strconv.Itoa(fromVersion),
		"to_version":   strconv.Itoa(targetSchema.Version),
	}
	_ = c.recordCheckpoint(rec)

	return targetSchema
}

func (c *ComputeEngine) bindPipelineState(t *protocol.TaskRequest, localSchema protocol.PipelineSchema) (protocol.PipelineSchema, error) {
	schemaHash := protocol.PipelineSchemaHash(localSchema)
	if t.PipelineState == nil {
		clonedSnapshot := protocol.ClonePipelineSchema(localSchema)
		t.PipelineState = &protocol.PipelineExecutionState{
			PipelineID:      localSchema.ID,
			PipelineVersion: localSchema.Version,
			SchemaHash:      schemaHash,
			Snapshot:        &clonedSnapshot,
			Outputs:         make(map[string]map[string]any),
			OutputProducers: map[string]string{"$initial": t.RequesterNodeID},
		}
		return localSchema, nil
	}

	incoming := t.PipelineState
	if incoming.PipelineID != localSchema.ID {
		return localSchema, fmt.Errorf("pipeline schema ID mismatch: task is bound to %q, local is %q",
			incoming.PipelineID, localSchema.ID)
	}

	// Resolve the task's starting snapshot
	var currentSchema protocol.PipelineSchema
	var hasSnapshot bool
	if incoming.Snapshot != nil {
		currentSchema = protocol.ClonePipelineSchema(*incoming.Snapshot)
		hasSnapshot = true
	} else if rev, exists := c.GetPipelineRevision(incoming.PipelineID); exists && protocol.PipelineSchemaHash(rev) == incoming.SchemaHash {
		currentSchema = rev
		cloned := protocol.ClonePipelineSchema(rev)
		incoming.Snapshot = &cloned
		hasSnapshot = true
	} else if schemaHash == incoming.SchemaHash {
		currentSchema = protocol.ClonePipelineSchema(localSchema)
		cloned := protocol.ClonePipelineSchema(localSchema)
		incoming.Snapshot = &cloned
		hasSnapshot = true
	}

	if !hasSnapshot {
		return localSchema, fmt.Errorf(
			"pipeline schema mismatch for '%s': task is bound to version %d hash %s, local schema is version %d hash %s",
			localSchema.ID, incoming.PipelineVersion, incoming.SchemaHash, localSchema.Version, schemaHash,
		)
	}

	execSchema := c.tryDynamicSchemaMigration(*t, incoming, currentSchema, localSchema)

	if incoming.CurrentStep > len(execSchema.Steps) {
		return execSchema, fmt.Errorf("step index %d out of bounds", incoming.CurrentStep)
	}
	if incoming.CurrentStep > 0 && len(execSchema.Steps) > 0 {
		previousStepID := execSchema.Steps[incoming.CurrentStep-1].ID
		if incoming.OutputProducers[previousStepID] == "" {
			return execSchema, fmt.Errorf("pipeline provenance missing for completed step '%s'", previousStepID)
		}
	}
	t.PipelineState = protocol.ClonePipelineExecutionState(incoming)
	if t.PipelineState.OutputProducers == nil {
		t.PipelineState.OutputProducers = make(map[string]string)
	}
	if t.PipelineState.OutputProducers["$initial"] == "" {
		t.PipelineState.OutputProducers["$initial"] = t.RequesterNodeID
	}
	return execSchema, nil
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
			} else {
				if stepOutputs, ok := pipelineState.Outputs[conn.FromStep]; ok {
					val = stepOutputs[conn.FromPort]
				}
			}
			inputProducers[conn.ToPort] = pipelineState.OutputProducers[conn.FromStep]
			stepPayload[conn.ToPort] = val
		}
	}

	if !hasConnections {
		if pipelineState.CurrentStep == 0 {
			protocol.CopyMapIf(stepPayload, t.Payload, func(k string, _ any) bool {
				return k != "$pipeline" && k != "requester_node_id"
			})
			protocol.PopulateMap(inputProducers, slices.Collect(maps.Keys(stepPayload)), pipelineState.OutputProducers["$initial"])
		} else {
			prevStepID := schema.Steps[pipelineState.CurrentStep-1].ID
			if prevOutputs, ok := pipelineState.Outputs[prevStepID]; ok {
				maps.Copy(stepPayload, prevOutputs)
				protocol.PopulateMap(inputProducers, slices.Collect(maps.Keys(prevOutputs)), pipelineState.OutputProducers[prevStepID])
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

		// Save intermediate output and record WAL checkpoint
		if c.taskJournal != nil {
			_ = c.taskJournal.SaveIntermediateOutput(t.TaskID, currentStep.ID, outputs)
		}
		rec := c.checkpointFromTask(t, "running")
		rec.StepID = currentStep.ID
		rec.StepIndex = pipelineState.CurrentStep
		rec.Outputs = protocol.CloneJSONMap(outputs)
		rec.Intermediate = true
		rec.PipelineState = protocol.ClonePipelineExecutionState(pipelineState)
		_ = c.recordCheckpoint(rec)

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
			if _, localSupports := c.registry.GetHandler(step.Service); localSupports {
				targetPeer = c.nodeID
			} else {
				exhaustErr := fmt.Errorf("failed to discover node for step '%s': %w", step.ID, err)
				if stepIndex > 0 && t.PipelineState != nil && (t.RequesterNodeID != "" || t.ReplyTo != "") {
					if escrowErr := c.escalateToEscrow(t, step, stepIndex, exhaustErr); escrowErr == nil {
						return
					} else {
						c.logger.Warn("Failed to escalate task to escrow", "taskID", t.TaskID, "step", step.ID, "error", escrowErr)
					}
				}
				c.sendPipelineError(t, exhaustErr)
				return
			}
		}
		if t.PipelineState != nil {
			if t.PipelineState.SelectedTargets == nil {
				t.PipelineState.SelectedTargets = make(map[string]string, len(schema.Steps))
			}
			t.PipelineState.SelectedTargets[step.ID] = targetPeer
		}
	}

	if targetPeer != "" {
		c.taskStatusesMu.Lock()
		meta := c.taskStatusMeta[t.TaskID]
		if meta.delegatedProducers == nil {
			meta.delegatedProducers = make(map[string]struct{})
		}
		meta.delegatedProducers[targetPeer] = struct{}{}
		c.taskStatusMeta[t.TaskID] = meta
		c.taskStatusesMu.Unlock()
	}

	err := c.dispatchStepWithFailover(t, step, targetPeer, stepIndex)
	if err != nil {
		if errors.Is(err, ErrTaskEscrowed) {
			return
		}
		c.sendPipelineError(t, err)
	}
}

func (c *ComputeEngine) dispatchStepWithFailover(t protocol.TaskRequest, step protocol.PipelineStep, initialTarget string, stepIndex int) error {
	triedPeers := make(map[string]bool)
	currentTarget := initialTarget

	if initialTarget != "" {
		c.taskStatusesMu.Lock()
		meta := c.taskStatusMeta[t.TaskID]
		if meta.delegatedProducers == nil {
			meta.delegatedProducers = make(map[string]struct{})
		}
		meta.delegatedProducers[initialTarget] = struct{}{}
		c.taskStatusMeta[t.TaskID] = meta
		c.taskStatusesMu.Unlock()
	}

	for range 3 {
		triedPeers[currentTarget] = true

		if currentTarget == c.nodeID {
			c.acceptedTasks.Delete(t.TaskID)
			t.ExpectedProducerNodeID = c.nodeID
			if t.PipelineState != nil {
				if t.PipelineState.SelectedTargets == nil {
					t.PipelineState.SelectedTargets = make(map[string]string)
				}
				t.PipelineState.SelectedTargets[step.ID] = c.nodeID
			}
			err := c.SubmitTask(t)
			if err == nil {
				return nil
			}
			c.logger.Warn("Local execution failed for pipeline step, attempting failover",
				"step", step.ID, "error", err)
		} else {
			if c.taskDispatcher == nil {
				return fmt.Errorf("task dispatcher not configured on compute engine")
			}
			t.ExpectedProducerNodeID = currentTarget
			if t.PipelineState != nil {
				if t.PipelineState.SelectedTargets == nil {
					t.PipelineState.SelectedTargets = make(map[string]string)
				}
				t.PipelineState.SelectedTargets[step.ID] = currentTarget
			}
			err := c.taskDispatcher(currentTarget, t)
			if err == nil {
				c.acceptedTasks.Delete(t.TaskID)
				return nil
			}
			c.logger.Warn("Failed to dispatch pipeline step to peer, attempting failover",
				"step", step.ID, "target", currentTarget, "error", err)
		}

		// Failover: query serviceFinder for an alternative candidate node offering the service
		var altTarget string
		if c.serviceFinder != nil {
			query := protocol.DiscoveryQuery{
				Service:       step.Service,
				ExcludedPeers: slices.Collect(maps.Keys(triedPeers)),
			}
			if stepIndex > 0 {
				query.RequiredCapabilities = map[string]int{
					protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
				}
			}
			peerID, _, _, findErr := c.serviceFinder(query)
			if findErr == nil && peerID != "" && !triedPeers[peerID] {
				altTarget = peerID
			}
		}

		// If serviceFinder didn't find an untried peer, check if local node can handle it
		if altTarget == "" && !triedPeers[c.nodeID] {
			if _, localSupports := c.registry.GetHandler(step.Service); localSupports {
				altTarget = c.nodeID
			}
		}

		if altTarget == "" {
			break
		}

		c.logger.Info("Failing over pipeline step to candidate node",
			"step", step.ID, "previous", currentTarget, "alternative", altTarget)
		c.taskStatusesMu.Lock()
		meta := c.taskStatusMeta[t.TaskID]
		if meta.delegatedProducers == nil {
			meta.delegatedProducers = make(map[string]struct{})
		}
		meta.delegatedProducers[altTarget] = struct{}{}
		c.taskStatusMeta[t.TaskID] = meta
		c.taskStatusesMu.Unlock()
		currentTarget = altTarget
	}

	failErr := fmt.Errorf("failed to dispatch step '%s': all target candidates exhausted (initial: %s)", step.ID, initialTarget)
	if stepIndex > 0 && t.PipelineState != nil && (t.RequesterNodeID != "" || t.ReplyTo != "") {
		if escrowErr := c.escalateToEscrow(t, step, stepIndex, failErr); escrowErr == nil {
			return ErrTaskEscrowed
		} else {
			c.logger.Warn("Failed to escalate task to escrow", "taskID", t.TaskID, "step", step.ID, "error", escrowErr)
		}
	}

	return failErr
}

func (c *ComputeEngine) advancePipeline(t protocol.TaskRequest, schema protocol.PipelineSchema) {
	pipelineState := t.PipelineState
	if pipelineState == nil {
		c.sendPipelineError(t, fmt.Errorf("pipeline execution state is missing"))
		return
	}

	// Dynamic in-flight schema migration check before executing next step
	if latest, exists := c.GetPipeline(t.Service); exists {
		schema = c.tryDynamicSchemaMigration(t, pipelineState, schema, latest)
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

		rec := c.checkpointFromTask(t, "completed")
		rec.StepID = finalStepID
		rec.StepIndex = pipelineState.CurrentStep
		rec.Outputs = protocol.CloneJSONMap(finalOutputs)
		rec.Intermediate = false
		rec.PipelineState = protocol.ClonePipelineExecutionState(pipelineState)
		_ = c.recordCheckpoint(rec)

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
	var stepID string
	var stepIndex int
	if t.PipelineState != nil {
		stepIndex = t.PipelineState.CurrentStep
		if stepIndex > 0 && t.PipelineState.Snapshot != nil && stepIndex-1 < len(t.PipelineState.Snapshot.Steps) {
			stepID = t.PipelineState.Snapshot.Steps[stepIndex-1].ID
		}
	}
	rec := c.checkpointFromTask(t, "failed")
	rec.StepID = stepID
	rec.StepIndex = stepIndex
	rec.Error = err.Error()
	_ = c.recordCheckpoint(rec)

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
		schema, exists := c.GetPipeline(req.Service)
		if !exists && req.PipelineState.Snapshot != nil {
			schema = *req.PipelineState.Snapshot
			exists = true
		}
		if exists {
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
		protocol.PopulateMapIf(delegatedProducers, slices.Collect(maps.Values(req.PipelineState.SelectedTargets)), struct{}{}, func(target string) bool {
			return target != ""
		})
	}
	c.taskStatusesMu.Lock()
	defer c.taskStatusesMu.Unlock()
	if _, exists := c.taskStatuses.Load(req.TaskID); exists {
		metadata := c.taskStatusMeta[req.TaskID]
		if metadata.delegatedProducers == nil {
			metadata.delegatedProducers = make(map[string]struct{})
		}
		utils.MergeMap(metadata.delegatedProducers, delegatedProducers, true)
		if req.ExpectedProducerNodeID != "" {
			metadata.expectedProducer = req.ExpectedProducerNodeID
			metadata.delegatedProducers[req.ExpectedProducerNodeID] = struct{}{}
		}
		c.taskStatusMeta[req.TaskID] = metadata
		return
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

// markTaskFailedShape stamps the terminal failure fields, mirroring the reason
// into the error output (SSOT failure shape for task statuses).
func markTaskFailedShape(response *protocol.ServiceTaskResponse, reason string) {
	response.Status = "failed"
	response.Error = reason
	response.Outputs = map[string]any{"error": reason}
}

func failedTaskResponse(taskID, service, reason string) protocol.ServiceTaskResponse {
	response := protocol.ServiceTaskResponse{TaskID: taskID, Service: service}
	markTaskFailedShape(&response, reason)
	return response
}

func (c *ComputeEngine) MarkTaskAsFailed(req protocol.TaskRequest, reason string) {
	c.setTaskStatus(failedTaskResponse(req.TaskID, req.Service, reason))
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
	meta := c.taskStatusMeta[response.TaskID]
	expectedProducer := meta.expectedProducer
	_, delegated := meta.delegatedProducers[producerNodeID]
	if (expectedProducer == "" || expectedProducer != producerNodeID) && !delegated {
		if expectedProducer == "" && len(meta.delegatedProducers) == 0 {
			return fmt.Errorf("%w: no producer was registered for task %q", ErrTaskProducer, response.TaskID)
		}
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
	c.recordTerminalResponseCheckpoint(response, producerNodeID)
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
	c.recordTerminalResponseCheckpoint(response, response.ProducerNodeID)
}

func (c *ComputeEngine) setTaskStatus(response protocol.ServiceTaskResponse) {
	response = protocol.CloneServiceTaskResponse(response)
	c.lifecycleMu.RLock()
	closed := c.closed
	c.lifecycleMu.RUnlock()
	if closed {
		markTaskFailedShape(&response, ErrClosed.Error())
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
	c.touchStatusMeta(c.taskStatusMeta, taskID, now)
}

// touchStatusMeta bumps the shared eviction sequence for one task's metadata
// entry in either bookkeeping map (active or retired).
func (c *ComputeEngine) touchStatusMeta(meta map[string]taskStatusMetadata, taskID string, now time.Time) {
	c.taskStatusSeq++
	metadata := meta[taskID]
	metadata.touchedAt = now
	metadata.sequence = c.taskStatusSeq
	meta[taskID] = metadata
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
	markTaskFailedShape(&response, reason)
	c.retiredTaskStatuses[taskID] = protocol.CloneServiceTaskResponse(response)
	c.retiredTaskStatusMeta[taskID] = metadata
	c.touchStatusMeta(c.retiredTaskStatusMeta, taskID, now)
	c.acceptedTasks.Delete(taskID)
}

func (c *ComputeEngine) touchRetiredTaskStatusLocked(taskID string, now time.Time) {
	c.touchStatusMeta(c.retiredTaskStatusMeta, taskID, now)
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
			c.storeTaskStatusLocked(failedTaskResponse(req.TaskID, req.Service, ErrClosed.Error()))
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
