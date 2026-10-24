package compute

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/storage"
)

func coordinatorNodeID(t protocol.TaskRequest) string {
	if t.RequesterNodeID != "" {
		return t.RequesterNodeID
	}
	if t.PipelineState != nil && t.PipelineState.OutputProducers != nil {
		if initial := t.PipelineState.OutputProducers["$initial"]; initial != "" {
			return initial
		}
	}
	if t.ReplyTo != "" {
		if u, err := url.Parse(t.ReplyTo); err == nil && u.Hostname() != "" {
			if peerID, ok := protocol.ParsePeerLocalHost(u.Hostname()); ok {
				return peerID
			}
			return u.Hostname()
		}
	}
	return ""
}

func (c *ComputeEngine) escalateToEscrow(t protocol.TaskRequest, step protocol.PipelineStep, stepIndex int, cause error) error {
	coordinator := coordinatorNodeID(t)

	escrowReq := protocol.TaskEscrowRequest{
		TaskID:          t.TaskID,
		Service:         t.Service,
		RequesterNodeID: coordinator,
		ReplyTo:         t.ReplyTo,
		StepID:          step.ID,
		StepIndex:       stepIndex,
		Status:          protocol.TaskStatusEscrow,
		Error:           cause.Error(),
		ProducerNodeID:  c.nodeID,
		Payload:         protocol.CloneJSONMap(t.Payload),
		PipelineState:   protocol.ClonePipelineExecutionState(t.PipelineState),
		Timestamp:       time.Now().UTC(),
	}
	if t.PipelineState != nil && t.PipelineState.Outputs != nil {
		escrowReq.Outputs = protocol.CloneMap(t.PipelineState.Outputs, protocol.CloneJSONMap)
	}

	// If coordinator is this node, handle internally
	if coordinator == c.nodeID || coordinator == "" {
		c.logger.Info("Coordinator putting task into local escrow", "taskID", t.TaskID, "step", step.ID)
		return c.PutTaskInEscrow(escrowReq)
	}

	// Intermediate worker: dispatch escrow request to coordinator
	c.logger.Info("Worker escalating task to coordinator escrow",
		"taskID", t.TaskID, "step", step.ID, "coordinator", coordinator)

	if c.taskEscrowDispatcher != nil {
		if err := c.taskEscrowDispatcher(coordinator, escrowReq); err != nil {
			return fmt.Errorf("escrow dispatcher: %w", err)
		}
	} else if c.peerClient != nil {
		ctx, cancel := context.WithTimeout(c.lifetimeCtx, protocol.RPCTimeoutSync)
		defer cancel()
		if err := c.peerClient.SendTaskEscrow(ctx, coordinator, escrowReq); err != nil {
			return fmt.Errorf("send task escrow RPC: %w", err)
		}
	} else {
		return fmt.Errorf("no escrow dispatcher or peer client available")
	}

	// Escrow accepted by coordinator: clean up local execution tracking on worker
	c.acceptedTasks.Delete(t.TaskID)
	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:         t.TaskID,
		Service:        t.Service,
		Status:         protocol.TaskStatusEscrow,
		Error:          cause.Error(),
		ProducerNodeID: c.nodeID,
	})
	rec := c.checkpointFromTask(t, protocol.TaskStatusEscrow)
	rec.StepID = step.ID
	rec.StepIndex = stepIndex
	rec.Error = cause.Error()
	_ = c.recordCheckpoint(rec)
	return nil
}

func checkpointFromEscrow(req protocol.TaskEscrowRequest, status, nodeID string) storage.TaskJournalRecord {
	return storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:          req.TaskID,
			Service:         req.Service,
			RequesterNodeID: req.RequesterNodeID,
			ReplyTo:         req.ReplyTo,
			Status:          status,
			Error:           req.Error,
			NodeID:          nodeID,
			Payload:         protocol.CloneJSONMap(req.Payload),
			PipelineState:   protocol.ClonePipelineExecutionState(req.PipelineState),
		},
		StepID:    req.StepID,
		StepIndex: req.StepIndex,
		Timestamp: time.Now().UTC(),
	}
}

// PutTaskInEscrow persists the task in bbolt with status "escrow" and enqueues it for wakeup.
func (c *ComputeEngine) PutTaskInEscrow(req protocol.TaskEscrowRequest) error {
	if req.TaskID == "" {
		return fmt.Errorf("empty task ID in escrow request")
	}
	req = protocol.CloneTaskEscrowRequest(req)
	req.Status = protocol.TaskStatusEscrow
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now().UTC()
	}
	if req.RequesterNodeID == "" {
		req.RequesterNodeID = c.nodeID
	}

	// Persist checkpoint to bbolt WAL + bucketTasksActive
	if c.taskJournal != nil {
		var stepOutputs map[string]any
		if req.PipelineState != nil && req.StepIndex > 0 {
			var prevStepID string
			if req.PipelineState.Snapshot != nil && req.StepIndex-1 < len(req.PipelineState.Snapshot.Steps) {
				prevStepID = req.PipelineState.Snapshot.Steps[req.StepIndex-1].ID
			} else if schema, exists := c.GetPipeline(req.Service); exists && req.StepIndex-1 < len(schema.Steps) {
				prevStepID = schema.Steps[req.StepIndex-1].ID
			}
			if prevStepID != "" && req.PipelineState.Outputs != nil {
				stepOutputs = req.PipelineState.Outputs[prevStepID]
			}
		}
		rec := checkpointFromEscrow(req, protocol.TaskStatusEscrow, req.ProducerNodeID)
		rec.Outputs = protocol.CloneJSONMap(stepOutputs)
		rec.Timestamp = req.Timestamp
		if err := c.recordCheckpoint(rec); err != nil {
			return fmt.Errorf("persist escrow checkpoint: %w", err)
		}
	}

	c.escrowTasksMu.Lock()
	c.escrowTasks[req.TaskID] = protocol.CloneTaskEscrowRequest(req)
	c.escrowTasksMu.Unlock()

	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:         req.TaskID,
		Service:        req.Service,
		Status:         protocol.TaskStatusEscrow,
		Error:          req.Error,
		ProducerNodeID: req.ProducerNodeID,
	})

	c.logger.Info("Task entered coordinator escrow", "taskID", req.TaskID, "step", req.StepID, "stepIndex", req.StepIndex)
	return nil
}

// GetEscrowTask returns an in-memory escrow task request by task ID.
func (c *ComputeEngine) GetEscrowTask(taskID string) (protocol.TaskEscrowRequest, bool) {
	c.escrowTasksMu.RLock()
	defer c.escrowTasksMu.RUnlock()
	task, exists := c.escrowTasks[taskID]
	if !exists {
		return protocol.TaskEscrowRequest{}, false
	}
	return protocol.CloneTaskEscrowRequest(task), true
}

// ListEscrowTasks returns all active escrow tasks sorted by TaskID.
func (c *ComputeEngine) ListEscrowTasks() []protocol.TaskEscrowRequest {
	c.escrowTasksMu.RLock()
	defer c.escrowTasksMu.RUnlock()
	list := make([]protocol.TaskEscrowRequest, 0, len(c.escrowTasks))
	for _, task := range c.escrowTasks {
		list = append(list, protocol.CloneTaskEscrowRequest(task))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].TaskID < list[j].TaskID
	})
	return list
}

// RestoreEscrowTask restores an escrow task record loaded from persistent storage into memory.
func (c *ComputeEngine) RestoreEscrowTask(task storage.TaskRecord) {
	c.escrowTasksMu.Lock()
	defer c.escrowTasksMu.Unlock()
	stepID := task.CurrentStepID
	if stepID == "" && task.PipelineState != nil && task.PipelineState.Snapshot != nil &&
		task.CurrentStep < len(task.PipelineState.Snapshot.Steps) {
		stepID = task.PipelineState.Snapshot.Steps[task.CurrentStep].ID
	}
	req := protocol.TaskEscrowRequest{
		TaskID:          task.TaskID,
		Service:         task.Service,
		RequesterNodeID: task.RequesterNodeID,
		ReplyTo:         task.ReplyTo,
		StepID:          stepID,
		StepIndex:       task.CurrentStep,
		Status:          protocol.TaskStatusEscrow,
		Error:           task.Error,
		Outputs:         task.Outputs,
		Payload:         protocol.CloneJSONMap(task.Payload),
		PipelineState:   protocol.ClonePipelineExecutionState(task.PipelineState),
		Timestamp:       task.UpdatedAt,
	}
	c.escrowTasks[task.TaskID] = req

	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:         task.TaskID,
		Service:        task.Service,
		Status:         protocol.TaskStatusEscrow,
		Error:          task.Error,
		ProducerNodeID: task.NodeID,
	})

	c.taskStatusesMu.Lock()
	meta := c.taskStatusMeta[task.TaskID]
	if meta.delegatedProducers == nil {
		meta.delegatedProducers = make(map[string]struct{})
	}
	if task.ExpectedProducerNodeID != "" {
		meta.expectedProducer = task.ExpectedProducerNodeID
		meta.delegatedProducers[task.ExpectedProducerNodeID] = struct{}{}
	}
	if task.NodeID != "" {
		meta.delegatedProducers[task.NodeID] = struct{}{}
	}
	c.taskStatusMeta[task.TaskID] = meta
	c.taskStatusesMu.Unlock()
}

// WakeupEscrowTasks iterates through all tasks in escrow and attempts to re-dispatch
// any that now have eligible candidates available in the mesh. Returns the number of resumed tasks.
func (c *ComputeEngine) WakeupEscrowTasks() int {
	c.escrowTasksMu.RLock()
	taskIDs := make([]string, 0, len(c.escrowTasks))
	for id := range c.escrowTasks {
		taskIDs = append(taskIDs, id)
	}
	c.escrowTasksMu.RUnlock()

	var resumed int
	for _, id := range taskIDs {
		if err := c.ResumeEscrowTask(id); err == nil {
			resumed++
		}
	}
	return resumed
}

// ResumeEscrowTask attempts to resume a specific task from escrow.
func (c *ComputeEngine) ResumeEscrowTask(taskID string) error {
	c.escrowTasksMu.Lock()
	task, exists := c.escrowTasks[taskID]
	if !exists {
		c.escrowTasksMu.Unlock()
		return fmt.Errorf("task %q not in escrow", taskID)
	}
	delete(c.escrowTasks, taskID)
	c.escrowTasksMu.Unlock()

	schema, schemaExists := c.GetPipeline(task.Service)
	if !schemaExists && task.PipelineState != nil && task.PipelineState.Snapshot != nil {
		schema = *task.PipelineState.Snapshot
		schemaExists = true
	}

	stepIndex := task.StepIndex
	var step protocol.PipelineStep
	if schemaExists && stepIndex >= 0 && stepIndex < len(schema.Steps) {
		step = schema.Steps[stepIndex]
	}

	revertEscrow := func() {
		c.escrowTasksMu.Lock()
		c.escrowTasks[taskID] = task
		c.escrowTasksMu.Unlock()

		c.setTaskStatus(protocol.ServiceTaskResponse{
			TaskID:         task.TaskID,
			Service:        task.Service,
			Status:         protocol.TaskStatusEscrow,
			Error:          task.Error,
			ProducerNodeID: task.ProducerNodeID,
		})

		rec := checkpointFromEscrow(task, protocol.TaskStatusEscrow, task.ProducerNodeID)
		rec.StepID = step.ID
		rec.StepIndex = stepIndex
		_ = c.recordCheckpoint(rec)
	}

	if !schemaExists {
		revertEscrow()
		return fmt.Errorf("pipeline schema %q not found for escrow task", task.Service)
	}

	if stepIndex < 0 || stepIndex >= len(schema.Steps) {
		revertEscrow()
		return fmt.Errorf("step index %d out of bounds for pipeline %q", stepIndex, task.Service)
	}

	// Find candidate node for the missing service
	var targetPeer string
	if _, localSupports := c.registry.GetHandler(step.Service); localSupports {
		targetPeer = c.nodeID
	} else if c.serviceFinder != nil {
		query := protocol.DiscoveryQuery{
			Service: step.Service,
		}
		if stepIndex > 0 {
			query.RequiredCapabilities = map[string]int{
				protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
			}
		}
		peerID, _, _, findErr := c.serviceFinder(query)
		if findErr == nil && peerID != "" {
			targetPeer = peerID
		}
	}

	if targetPeer == "" {
		revertEscrow()
		return fmt.Errorf("no candidate node available for step '%s' service '%s'", step.ID, step.Service)
	}

	c.logger.Info("Waking up escrow task for execution",
		"taskID", task.TaskID, "step", step.ID, "targetPeer", targetPeer)

	// Persist running checkpoint to WAL
	rec := checkpointFromEscrow(task, "running", c.nodeID)
	rec.StepID = step.ID
	rec.StepIndex = stepIndex
	_ = c.recordCheckpoint(rec)

	c.setTaskStatus(protocol.ServiceTaskResponse{
		TaskID:         task.TaskID,
		Service:        task.Service,
		Status:         "running",
		ProducerNodeID: targetPeer,
	})

	// Re-construct TaskRequest from escrow state
	taskReq := protocol.TaskRequest{
		TaskID:                 task.TaskID,
		Service:                task.Service,
		RequesterNodeID:        task.RequesterNodeID,
		ReplyTo:                task.ReplyTo,
		PipelineState:          protocol.ClonePipelineExecutionState(task.PipelineState),
		Payload:                protocol.CloneJSONMap(task.Payload),
		ExpectedProducerNodeID: targetPeer,
	}
	if taskReq.Payload == nil {
		taskReq.Payload = make(map[string]any)
	}
	if taskReq.PipelineState == nil {
		taskReq.PipelineState = &protocol.PipelineExecutionState{
			PipelineID:      schema.ID,
			PipelineVersion: schema.Version,
			CurrentStep:     stepIndex,
			Outputs:         make(map[string]map[string]any),
		}
	}
	if taskReq.PipelineState.SchemaHash == "" {
		taskReq.PipelineState.SchemaHash = protocol.PipelineSchemaHash(schema)
	}
	if taskReq.PipelineState.Snapshot == nil {
		clonedSnapshot := protocol.ClonePipelineSchema(schema)
		taskReq.PipelineState.Snapshot = &clonedSnapshot
	}
	if taskReq.PipelineState.Outputs == nil {
		taskReq.PipelineState.Outputs = make(map[string]map[string]any)
	}
	for k, v := range task.Outputs {
		if _, exists := taskReq.PipelineState.Outputs[k]; !exists {
			taskReq.PipelineState.Outputs[k] = protocol.CloneJSONMap(v)
		}
	}
	if taskReq.PipelineState.OutputProducers == nil {
		taskReq.PipelineState.OutputProducers = make(map[string]string)
	}
	if taskReq.PipelineState.OutputProducers["$initial"] == "" {
		taskReq.PipelineState.OutputProducers["$initial"] = task.RequesterNodeID
	}
	if stepIndex > 0 && task.ProducerNodeID != "" {
		prevStepID := schema.Steps[stepIndex-1].ID
		if taskReq.PipelineState.OutputProducers[prevStepID] == "" {
			taskReq.PipelineState.OutputProducers[prevStepID] = task.ProducerNodeID
		}
	}
	if taskReq.PipelineState.SelectedTargets == nil {
		taskReq.PipelineState.SelectedTargets = make(map[string]string)
	}
	taskReq.PipelineState.SelectedTargets[step.ID] = targetPeer

	// Prepare future pipeline targets if possible
	_ = c.PreparePipelineTargets(&taskReq)

	c.taskStatusesMu.Lock()
	meta := c.taskStatusMeta[task.TaskID]
	if meta.delegatedProducers == nil {
		meta.delegatedProducers = make(map[string]struct{})
	}
	meta.delegatedProducers[targetPeer] = struct{}{}
	meta.expectedProducer = targetPeer
	if taskReq.PipelineState != nil {
		for _, p := range taskReq.PipelineState.SelectedTargets {
			if p != "" {
				meta.delegatedProducers[p] = struct{}{}
			}
		}
	}
	c.taskStatusMeta[task.TaskID] = meta
	c.taskStatusesMu.Unlock()

	if targetPeer == c.nodeID {
		c.acceptedTasks.Delete(taskReq.TaskID)
		if err := c.SubmitTask(taskReq); err != nil {
			revertEscrow()
			return fmt.Errorf("resubmit local task failed: %w", err)
		}
		return nil
	}

	if c.taskDispatcher == nil {
		revertEscrow()
		return fmt.Errorf("task dispatcher not configured on compute engine")
	}

	if err := c.taskDispatcher(targetPeer, taskReq); err != nil {
		revertEscrow()
		return fmt.Errorf("dispatch step to %q failed: %w", targetPeer, err)
	}

	return nil
}

// StartEscrowSupervisor launches a periodic supervisor that re-checks escrow tasks
// on a timer in addition to peer announcement triggers.
func (c *ComputeEngine) StartEscrowSupervisor(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	c.escrowSupervisorOnce.Do(func() {
		c.wg.Go(func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-c.lifetimeCtx.Done():
					return
				case <-ticker.C:
					c.WakeupEscrowTasks()
				}
			}
		})
	})
}
