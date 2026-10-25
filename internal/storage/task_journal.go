package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/bbolt"
	"proxyma/internal/protocol"
	storage "proxyma/internal/storage/physical"
)

var (
	ErrEmptyTaskID = errors.New("task ID cannot be empty")
	ErrTaskActive = errors.New("cannot prune intermediate data for active task")
	ErrTaskTerminal = errors.New("task is in terminal state")
	ErrTaskNotFound = errors.New("task not found")
)

// TaskContext centralizes shared task identity, provenance, routing, and execution state
// across WAL journal checkpoints and materialized active/history task aggregates.
type TaskContext struct {
	TaskID          string                           `json:"task_id"`
	Service         string                           `json:"service,omitempty"`
	Status          string                           `json:"status"` // e.g. "pending", "running", "completed", "failed", "escrow"
	RequesterNodeID string                           `json:"requester_node_id,omitempty"`
	ReplyTo         string                           `json:"reply_to,omitempty"`
	NodeID          string                           `json:"node_id,omitempty"`
	Payload         map[string]any                   `json:"payload,omitempty"`
	Error           string                           `json:"error,omitempty"`
	PipelineState   *protocol.PipelineExecutionState `json:"pipeline_state,omitempty"`
	Metadata        map[string]string                `json:"metadata,omitempty"`
}

func validateTaskID(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return ErrEmptyTaskID
	}
	return nil
}

func mergeComparable[T comparable](dst *T, src T, overwrite bool) {
	var zero T
	if src == zero {
		return
	}
	if overwrite || *dst == zero {
		*dst = src
	}
}

func (c *TaskContext) merge(src TaskContext, overwrite bool) {
	if overwrite && src.Status != "" {
		c.Status = src.Status
	}
	mergeComparable(&c.Service, src.Service, overwrite)
	mergeComparable(&c.RequesterNodeID, src.RequesterNodeID, overwrite)
	mergeComparable(&c.ReplyTo, src.ReplyTo, overwrite)
	mergeComparable(&c.NodeID, src.NodeID, overwrite)
	mergeComparable(&c.Error, src.Error, overwrite)
	if src.Payload != nil && (overwrite || c.Payload == nil) {
		c.Payload = protocol.CloneJSONMap(src.Payload)
	}
	if src.PipelineState != nil && (overwrite || c.PipelineState == nil) {
		c.PipelineState = protocol.ClonePipelineExecutionState(src.PipelineState)
	}
	if len(src.Metadata) > 0 {
		if c.Metadata == nil {
			c.Metadata = make(map[string]string, len(src.Metadata))
		}
		protocol.MergeMap(c.Metadata, src.Metadata, overwrite)
	}
}

// Overlay updates non-empty and non-nil fields from update onto c.
func (c *TaskContext) Overlay(update TaskContext) {
	c.merge(update, true)
}

// FillMissing copies values from base into empty or nil fields on c.
func (c *TaskContext) FillMissing(base TaskContext) {
	c.merge(base, false)
}

// TaskJournalRecord represents an immutable point-in-time state checkpoint
// written to the WAL for task and pipeline execution.
type TaskJournalRecord struct {
	TaskContext
	Sequence          uint64         `json:"sequence"`
	Timestamp         time.Time      `json:"timestamp"`
	StepID            string         `json:"step_id,omitempty"`
	StepIndex         int            `json:"step_index"`
	Outputs           map[string]any `json:"outputs,omitempty"`
	Intermediate      bool           `json:"intermediate,omitempty"`
	IntermediateBlobs []string       `json:"intermediate_blobs,omitempty"`
}

// BlobHashes extracts referenced CAS blob hashes from journal checkpoint outputs and intermediate blobs.
func (r *TaskJournalRecord) BlobHashes() []string {
	var hashes []string
	hashes = append(hashes, r.IntermediateBlobs...)
	hashes = append(hashes, ExtractBlobHashes(r.Outputs)...)
	return hashes
}

// TaskRecord captures the active or historical state of a task in the cluster.
type TaskRecord struct {
	TaskContext
	CreatedAt              time.Time                 `json:"created_at"`
	UpdatedAt              time.Time                 `json:"updated_at"`
	CurrentStep            int                       `json:"current_step"`
	CurrentStepID          string                    `json:"current_step_id,omitempty"`
	ExpectedProducerNodeID string                    `json:"expected_producer_node_id,omitempty"`
	Outputs                map[string]map[string]any `json:"outputs,omitempty"`
	FinalOutputs           map[string]any            `json:"final_outputs,omitempty"`
	Retries                int                       `json:"retries,omitempty"`
}

// ActiveTaskRecord is an alias of TaskRecord for active in-flight or escrow tasks.
type ActiveTaskRecord = TaskRecord

// BlobHashes extracts all referenced CAS blob hashes from payload, step outputs, and final outputs.
func (t *TaskRecord) BlobHashes() []string {
	var hashes []string
	hashes = append(hashes, ExtractBlobHashes(t.Outputs)...)
	hashes = append(hashes, ExtractBlobHashes(t.Payload)...)
	hashes = append(hashes, ExtractBlobHashes(t.FinalOutputs)...)
	return hashes
}

func newTaskRecord(taskID string, createdAt time.Time) TaskRecord {
	return TaskRecord{
		TaskContext: TaskContext{
			TaskID: taskID,
		},
		CreatedAt: createdAt,
		Outputs:   make(map[string]map[string]any),
	}
}

func (t *TaskRecord) applyJournalRecord(record TaskJournalRecord) {
	prevStatus := t.Status
	t.Overlay(record.TaskContext)
	if record.Status == protocol.TaskEventSchemaMigrated {
		if prevStatus != "" {
			t.Status = prevStatus
		} else {
			t.Status = "running"
		}
	}
	t.UpdatedAt = record.Timestamp
	if record.StepID != "" {
		t.CurrentStepID = record.StepID
	}
	if record.StepIndex > 0 || t.CurrentStep == 0 {
		t.CurrentStep = record.StepIndex
	}
	if record.PipelineState != nil && record.PipelineState.Outputs != nil {
		clonedOutputs := protocol.CloneMap(record.PipelineState.Outputs, protocol.CloneJSONMap)
		if t.Outputs == nil {
			t.Outputs = clonedOutputs
		} else {
			protocol.MergeMap(t.Outputs, clonedOutputs, true)
		}
	}
	if record.Outputs != nil {
		if isTerminalTaskStatus(record.Status) {
			t.FinalOutputs = protocol.CloneJSONMap(record.Outputs)
		}
		if record.StepID != "" {
			if t.Outputs == nil {
				t.Outputs = make(map[string]map[string]any)
			}
			t.Outputs[record.StepID] = protocol.CloneJSONMap(record.Outputs)
		}
	}
}

func (t *TaskRecord) mergeFromActive(active TaskRecord) {
	t.FillMissing(active.TaskContext)
	if t.CreatedAt.IsZero() && !active.CreatedAt.IsZero() {
		t.CreatedAt = active.CreatedAt
	}
	if t.Outputs == nil && active.Outputs != nil {
		t.Outputs = protocol.CloneMap(active.Outputs, protocol.CloneJSONMap)
	}
	mergeComparable(&t.CurrentStepID, active.CurrentStepID, false)
	mergeComparable(&t.ExpectedProducerNodeID, active.ExpectedProducerNodeID, false)
	mergeComparable(&t.CurrentStep, active.CurrentStep, false)
	mergeComparable(&t.Retries, active.Retries, false)
}

func isTerminalTaskStatus(status string) bool {
	return status == "completed" || status == "failed"
}

func scanTaskJournalPrefix(journalBucket *bbolt.Bucket, taskID string, fn func(key, val []byte) error) error {
	prefix := []byte(taskID + "/")
	c := journalBucket.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func scanTaskJournalRecords(journalBucket *bbolt.Bucket, taskID string, fn func(key []byte, rec TaskJournalRecord) error) error {
	return scanTaskJournalPrefix(journalBucket, taskID, func(k, v []byte) error {
		var rec TaskJournalRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return fmt.Errorf("corrupt task journal record %s: %w", string(k), err)
		}
		return fn(k, rec)
	})
}

func taskJournalKey(taskID string, seq uint64) []byte {
	return fmt.Appendf(nil, "%s/%020d", taskID, seq)
}

func parseTaskJournalSeq(key []byte, taskID string) (uint64, bool) {
	prefix := taskID + "/"
	str := string(key)
	if !strings.HasPrefix(str, prefix) {
		return 0, false
	}
	seqStr := strings.TrimPrefix(str, prefix)
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// ExtractBlobHashes searches nested outputs, maps, slices, and strings for CAS blob hashes.
func ExtractBlobHashes(v any) []string {
	return extractBlobHashesInternal(v, false)
}

func extractNonIntermediateBlobHashes(v any) []string {
	return extractBlobHashesInternal(v, true)
}

func extractBlobHashesInternal(v any, skipPipelineOutputs bool) []string {
	seen := make(map[string]bool)
	var result []string

	addHash := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if storage.IsValidCASHash(h) && !seen[h] {
			seen[h] = true
			result = append(result, h)
		}
	}

	var walk func(val any)
	walk = func(val any) {
		if val == nil {
			return
		}
		switch x := val.(type) {
		case string:
			if h, ok := protocol.ParseVFSURI(x); ok {
				addHash(h)
			} else if storage.IsValidCASHash(x) {
				addHash(x)
			}
		case map[string]map[string]any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if h, ok := x[protocol.OutputHashKey].(string); ok {
				addHash(h)
			}
			for k, item := range x {
				if skipPipelineOutputs && k == "$pipeline_outputs" {
					continue
				}
				walk(item)
			}
		case map[string]string:
			for _, item := range x {
				walk(item)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		case []string:
			for _, item := range x {
				walk(item)
			}
		}
	}

	walk(v)
	return result
}

// AppendTaskCheckpoint records a new journal checkpoint in the WAL and synchronizes
// the corresponding active or history task record.
func (se *StorageEngine) AppendTaskCheckpoint(record TaskJournalRecord) error {
	if err := validateTaskID(record.TaskID); err != nil {
		return err
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	if record.Intermediate && len(record.IntermediateBlobs) == 0 && len(record.Outputs) > 0 {
		record.IntermediateBlobs = ExtractBlobHashes(record.Outputs)
	}

	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		// 1. Guard against terminal state regression: terminal tasks cannot regress
		hist, histExists, err := boltGetJSON[TaskRecord](tx, bucketTasksHistory, record.TaskID)
		if err != nil {
			return err
		}
		if histExists && isTerminalTaskStatus(hist.Status) {
			if record.Status != hist.Status {
				return ErrTaskTerminal
			}
		}

		journalBucket, err := boltBucket(tx, bucketTasksJournal)
		if err != nil {
			return err
		}

		if record.Sequence == 0 {
			var maxSeq uint64
			_ = scanTaskJournalPrefix(journalBucket, record.TaskID, func(k, _ []byte) error {
				if seq, ok := parseTaskJournalSeq(k, record.TaskID); ok && seq > maxSeq {
					maxSeq = seq
				}
				return nil
			})
			record.Sequence = maxSeq + 1
		}

		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal task checkpoint: %w", err)
		}
		if err := journalBucket.Put(taskJournalKey(record.TaskID, record.Sequence), encoded); err != nil {
			return fmt.Errorf("persist task journal checkpoint: %w", err)
		}

		active, activeExists, err := boltGetJSON[TaskRecord](tx, bucketTasksActive, record.TaskID)
		if err != nil {
			return err
		}

		// Reconcile active vs history
		terminal := isTerminalTaskStatus(record.Status)
		if terminal {
			if activeExists {
				if err := boltDelete(tx, bucketTasksActive, record.TaskID); err != nil {
					return err
				}
			}

			var history TaskRecord
			if histExists {
				history = hist
				if activeExists {
					history.mergeFromActive(active)
				}
			} else if activeExists {
				history = active
			} else {
				history = newTaskRecord(record.TaskID, record.Timestamp)
			}

			history.applyJournalRecord(record)
			return boltPutJSON(tx, bucketTasksHistory, record.TaskID, history)
		}

		// Non-terminal: maintain bucketTasksActive
		if !activeExists {
			active = newTaskRecord(record.TaskID, record.Timestamp)
		}
		active.applyJournalRecord(record)
		return boltPutJSON(tx, bucketTasksActive, record.TaskID, active)
	})
}

// SaveIntermediateOutput stages a step output checkpoint flagged as intermediate.
func (se *StorageEngine) SaveIntermediateOutput(taskID string, stepID string, outputs map[string]any) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	blobs := ExtractBlobHashes(outputs)
	record := TaskJournalRecord{
		TaskContext: TaskContext{
			TaskID: taskID,
			Status: "running",
		},
		StepID:            stepID,
		Outputs:           protocol.CloneJSONMap(outputs),
		Intermediate:      true,
		IntermediateBlobs: blobs,
		Timestamp:         time.Now().UTC(),
	}
	return se.AppendTaskCheckpoint(record)
}

// GetTaskJournal returns all journal records for taskID in sequence order.
func (se *StorageEngine) GetTaskJournal(taskID string) ([]TaskJournalRecord, error) {
	if err := validateTaskID(taskID); err != nil {
		return nil, err
	}
	var records []TaskJournalRecord
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		journalBucket, err := boltBucket(tx, bucketTasksJournal)
		if err != nil {
			return err
		}
		return scanTaskJournalRecords(journalBucket, taskID, func(_ []byte, rec TaskJournalRecord) error {
			records = append(records, rec)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Sequence < records[j].Sequence
	})
	return records, nil
}

// GetLatestTaskCheckpoint returns the highest-sequence checkpoint recorded for taskID.
func (se *StorageEngine) GetLatestTaskCheckpoint(taskID string) (TaskJournalRecord, bool, error) {
	records, err := se.GetTaskJournal(taskID)
	if err != nil {
		return TaskJournalRecord{}, false, err
	}
	if len(records) == 0 {
		return TaskJournalRecord{}, false, nil
	}
	return records[len(records)-1], true, nil
}

// PruneIntermediateTaskData removes intermediate checkpoint events from the journal
// and triggers physical garbage collection for intermediate CAS blobs, keeping final
// outputs, terminal task records, and other tasks' data intact.
func (se *StorageEngine) PruneIntermediateTaskData(taskID string) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}

	// Read-only pre-flight inspection: check task status and verify if intermediate
	// records exist before acquiring full DB write lock and scanning history.
	var hasIntermediate bool
	err := se.subscriptions.View(func(tx *bbolt.Tx) error {
		isActive, err := boltHasKey(tx, bucketTasksActive, taskID)
		if err != nil {
			return err
		}
		if isActive {
			return ErrTaskActive
		}

		_, histExists, err := boltGetJSON[TaskRecord](tx, bucketTasksHistory, taskID)
		if err != nil {
			return err
		}

		journalBucket := tx.Bucket([]byte(bucketTasksJournal))
		var hasTerminalJournal bool
		if journalBucket != nil {
			errStop := errors.New("stop")
			err := scanTaskJournalRecords(journalBucket, taskID, func(_ []byte, rec TaskJournalRecord) error {
				if isTerminalTaskStatus(rec.Status) {
					hasTerminalJournal = true
				}
				if (rec.Intermediate || !isTerminalTaskStatus(rec.Status)) && rec.Status != protocol.TaskEventSchemaMigrated {
					hasIntermediate = true
				}
				if hasIntermediate && (histExists || hasTerminalJournal) {
					return errStop
				}
				return nil
			})
			if err != nil && !errors.Is(err, errStop) {
				return err
			}
		}

		if !histExists && !hasTerminalJournal {
			return ErrTaskNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !hasIntermediate {
		return nil
	}

	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()

	var intermediateBlobsSet = make(map[string]bool)

	err = se.subscriptions.Update(func(tx *bbolt.Tx) error {
		// 1. Guard: Check if task is active. Active tasks cannot have intermediate data pruned.
		isActive, err := boltHasKey(tx, bucketTasksActive, taskID)
		if err != nil {
			return err
		}
		if isActive {
			return ErrTaskActive
		}

		// 2. Verify task existence in history or terminal journal
		hist, histExists, err := boltGetJSON[TaskRecord](tx, bucketTasksHistory, taskID)
		if err != nil {
			return err
		}

		journalBucket, err := boltBucket(tx, bucketTasksJournal)
		if err != nil {
			return err
		}

		var hasTerminalJournal bool
		type journalEntry struct {
			key []byte
			rec TaskJournalRecord
		}
		var journalRecords []journalEntry

		_ = scanTaskJournalRecords(journalBucket, taskID, func(k []byte, rec TaskJournalRecord) error {
			if isTerminalTaskStatus(rec.Status) {
				hasTerminalJournal = true
			}
			journalRecords = append(journalRecords, journalEntry{
				key: bytes.Clone(k),
				rec: rec,
			})
			return nil
		})

		if !histExists && !hasTerminalJournal {
			return ErrTaskNotFound
		}

		// 3. Collect protected final blobs for this task (ignoring aggregate $pipeline_outputs)
		finalBlobs := make(map[string]bool)
		if histExists {
			protocol.PopulateMap(finalBlobs, extractNonIntermediateBlobHashes(hist.FinalOutputs), true)
			protocol.PopulateMap(finalBlobs, ExtractBlobHashes(hist.Payload), true)
		}
		for _, item := range journalRecords {
			if !item.rec.Intermediate && isTerminalTaskStatus(item.rec.Status) {
				protocol.PopulateMap(finalBlobs, extractNonIntermediateBlobHashes(item.rec.Outputs), true)
				protocol.PopulateMap(finalBlobs, ExtractBlobHashes(item.rec.Payload), true)
			}
		}

		// 4. Cross-task protection: collect blobs referenced by any other active task
		// or completed history records to prevent deleting deduplicated CAS blobs currently in use.
		activeBucket, err := boltBucket(tx, bucketTasksActive)
		if err != nil {
			return err
		}
		cActive := activeBucket.Cursor()
		for k, v := cActive.First(); k != nil; k, v = cActive.Next() {
			var otherActive TaskRecord
			if err := json.Unmarshal(v, &otherActive); err == nil {
				protocol.PopulateMap(finalBlobs, otherActive.BlobHashes(), true)
			}
		}

		histBucket, err := boltBucket(tx, bucketTasksHistory)
		if err != nil {
			return err
		}
		cHist := histBucket.Cursor()
		for k, v := cHist.First(); k != nil; k, v = cHist.Next() {
			if string(k) == taskID {
				continue
			}
			var otherHist TaskRecord
			if err := json.Unmarshal(v, &otherHist); err == nil {
				protocol.PopulateMap(finalBlobs, extractNonIntermediateBlobHashes(otherHist.FinalOutputs), true)
				protocol.PopulateMap(finalBlobs, ExtractBlobHashes(otherHist.Payload), true)
			}
		}

		// 5. Identify intermediate journal records and intermediate blobs to delete
		var intermediateKeys [][]byte
		for _, item := range journalRecords {
			rec := item.rec
			if (rec.Intermediate || !isTerminalTaskStatus(rec.Status)) && rec.Status != protocol.TaskEventSchemaMigrated {
				intermediateKeys = append(intermediateKeys, item.key)
				protocol.PopulateMapIf(intermediateBlobsSet, rec.BlobHashes(), true, func(h string) bool {
					return !finalBlobs[h]
				})
			}
		}

		// 6. Delete intermediate journal records from bbolt
		for _, key := range intermediateKeys {
			if err := journalBucket.Delete(key); err != nil {
				return err
			}
		}

		// 7. Delete transient staging VFS index entries and queue GC for intermediate blobs
		if len(intermediateBlobsSet) > 0 {
			if err := se.deleteStagedVFSIndexEntriesLocked(tx, intermediateBlobsSet); err != nil {
				return err
			}
			for hash := range intermediateBlobsSet {
				if err := boltPutFlag(tx, bucketPendingBlobGC, hash); err != nil {
					return err
				}
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	if len(intermediateBlobsSet) == 0 {
		return nil
	}

	// 8. Physically sweep queued orphans
	return se.sweepPendingBlobGCLocked()
}

// DeleteTaskJournal removes all journal records for taskID from bucketTasksJournal.
func (se *StorageEngine) DeleteTaskJournal(taskID string) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	return se.subscriptions.Update(func(tx *bbolt.Tx) error {
		journalBucket, err := boltBucket(tx, bucketTasksJournal)
		if err != nil {
			return err
		}
		var keysToDelete [][]byte
		_ = scanTaskJournalPrefix(journalBucket, taskID, func(k, _ []byte) error {
			keysToDelete = append(keysToDelete, bytes.Clone(k))
			return nil
		})
		for _, k := range keysToDelete {
			if err := journalBucket.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (se *StorageEngine) saveTaskRecord(bucket string, task TaskRecord) error {
	if err := validateTaskID(task.TaskID); err != nil {
		return err
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = time.Now().UTC()
	return se.boltPutKeyed(bucket, task.TaskID, task)
}

func (se *StorageEngine) getTaskRecord(bucket, taskID string) (TaskRecord, bool, error) {
	if err := validateTaskID(taskID); err != nil {
		return TaskRecord{}, false, err
	}
	return boltViewGetJSON[TaskRecord](se.subscriptions, bucket, taskID)
}

func (se *StorageEngine) deleteTaskRecord(bucket, taskID string) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	return se.boltDeleteKeyed(bucket, taskID)
}

func (se *StorageEngine) loadTaskRecords(bucket string) (map[string]TaskRecord, error) {
	return boltLoadMapJSON[TaskRecord](se.subscriptions, bucket)
}

func (se *StorageEngine) listTaskRecords(bucket string, sortByUpdated bool) ([]TaskRecord, error) {
	taskMap, err := se.loadTaskRecords(bucket)
	if err != nil {
		return nil, err
	}
	tasks := slices.Collect(maps.Values(taskMap))
	sort.Slice(tasks, func(i, j int) bool {
		if sortByUpdated {
			if !tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
				return tasks[i].UpdatedAt.Before(tasks[j].UpdatedAt)
			}
		} else {
			if !tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
				return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
			}
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
	return tasks, nil
}

// SaveActiveTask persists an active task record in bucketTasksActive.
func (se *StorageEngine) SaveActiveTask(task TaskRecord) error {
	return se.saveTaskRecord(bucketTasksActive, task)
}

// GetActiveTask retrieves an active task record by task ID.
func (se *StorageEngine) GetActiveTask(taskID string) (TaskRecord, bool, error) {
	return se.getTaskRecord(bucketTasksActive, taskID)
}

// DeleteActiveTask removes an active task record from bucketTasksActive.
func (se *StorageEngine) DeleteActiveTask(taskID string) error {
	return se.deleteTaskRecord(bucketTasksActive, taskID)
}

// LoadActiveTasks loads all currently active tasks from bucketTasksActive.
func (se *StorageEngine) LoadActiveTasks() (map[string]TaskRecord, error) {
	return se.loadTaskRecords(bucketTasksActive)
}

// ListActiveTasks returns all active tasks as a slice.
func (se *StorageEngine) ListActiveTasks() ([]TaskRecord, error) {
	return se.listTaskRecords(bucketTasksActive, false)
}

// SaveTaskHistory persists a completed or failed task record in bucketTasksHistory.
func (se *StorageEngine) SaveTaskHistory(task TaskRecord) error {
	return se.saveTaskRecord(bucketTasksHistory, task)
}

// GetTaskHistory retrieves a completed or failed task record by task ID.
func (se *StorageEngine) GetTaskHistory(taskID string) (TaskRecord, bool, error) {
	return se.getTaskRecord(bucketTasksHistory, taskID)
}

// DeleteTaskHistory removes a task record from bucketTasksHistory.
func (se *StorageEngine) DeleteTaskHistory(taskID string) error {
	return se.deleteTaskRecord(bucketTasksHistory, taskID)
}

// LoadTaskHistory loads all history tasks from bucketTasksHistory.
func (se *StorageEngine) LoadTaskHistory() (map[string]TaskRecord, error) {
	return se.loadTaskRecords(bucketTasksHistory)
}

// ListTaskHistory returns all history tasks as a slice.
func (se *StorageEngine) ListTaskHistory() ([]TaskRecord, error) {
	return se.listTaskRecords(bucketTasksHistory, true)
}
