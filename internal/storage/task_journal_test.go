package storage_test

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/testutil"
)

func TestTaskJournalValidation(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-val")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{TaskID: ""},
	})
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.SaveIntermediateOutput("", "step-0", map[string]any{"key": "val"})
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	_, err = engine.GetTaskJournal("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	_, _, err = engine.GetLatestTaskCheckpoint("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.PruneIntermediateTaskData("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.DeleteTaskJournal("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.SaveActiveTask(storage.TaskRecord{
		TaskContext: storage.TaskContext{TaskID: ""},
	})
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	_, _, err = engine.GetActiveTask("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.DeleteActiveTask("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.SaveTaskHistory(storage.TaskRecord{
		TaskContext: storage.TaskContext{TaskID: ""},
	})
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	_, _, err = engine.GetTaskHistory("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)

	err = engine.DeleteTaskHistory("")
	require.ErrorIs(t, err, storage.ErrEmptyTaskID)
}

func TestTaskJournalMonotonicSequencingAndActiveTracking(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-seq")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	taskID := "task-seq-001"

	// 1. Initial pending checkpoint with metadata
	err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:   taskID,
			Status:   "pending",
			Metadata: map[string]string{"initiator": "client"},
		},
		StepIndex: 0,
	})
	require.NoError(t, err)

	active, exists, err := engine.GetActiveTask(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "pending", active.Status)
	require.Equal(t, 0, active.CurrentStep)
	require.Equal(t, "client", active.Metadata["initiator"])

	// 2. Step 0 checkpoint
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "running",
		},
		StepID:    "step-0",
		StepIndex: 0,
		Outputs:   map[string]any{"partial": 42},
	})
	require.NoError(t, err)

	active, exists, err = engine.GetActiveTask(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "running", active.Status)
	require.Equal(t, "step-0", active.CurrentStepID)
	require.NotNil(t, active.Outputs["step-0"])
	require.Equal(t, "client", active.Metadata["initiator"])

	// 3. Step 1 intermediate output
	err = engine.SaveIntermediateOutput(taskID, "step-1", map[string]any{"result": "step1-complete"})
	require.NoError(t, err)

	active, exists, err = engine.GetActiveTask(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "running", active.Status)
	require.Equal(t, "step-1", active.CurrentStepID)
	require.Equal(t, "step1-complete", active.Outputs["step-1"]["result"])

	// Verify journal sequence order
	journal, err := engine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journal, 3)
	require.Equal(t, uint64(1), journal[0].Sequence)
	require.Equal(t, "pending", journal[0].Status)
	require.Equal(t, uint64(2), journal[1].Sequence)
	require.Equal(t, "step-0", journal[1].StepID)
	require.Equal(t, uint64(3), journal[2].Sequence)
	require.Equal(t, "step-1", journal[2].StepID)
	require.True(t, journal[2].Intermediate)

	latest, found, err := engine.GetLatestTaskCheckpoint(taskID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(3), latest.Sequence)
	require.Equal(t, "step-1", latest.StepID)

	// 4. Complete the task
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
		StepIndex: 2,
		Outputs:   map[string]any{"final_answer": "42"},
	})
	require.NoError(t, err)

	// Active task should be deleted
	_, exists, err = engine.GetActiveTask(taskID)
	require.NoError(t, err)
	require.False(t, exists, "completed task should be removed from active bucket")

	// Task should now exist in history
	hist, exists, err := engine.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists, "completed task should be recorded in history bucket")
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "42", hist.FinalOutputs["final_answer"])
	require.NotNil(t, hist.Outputs["step-0"])
	require.NotNil(t, hist.Outputs["step-1"])
	require.Equal(t, "client", hist.Metadata["initiator"])
}

func TestTaskJournalBlobExtractionAndIntermediatePruning(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-prune")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	contentInterm1 := []byte("intermediate data step 0")
	hashInterm1, _, err := engine.SavePhysicalBlob(bytes.NewReader(contentInterm1))
	require.NoError(t, err)

	contentInterm2 := []byte("intermediate data step 1")
	hashInterm2, _, err := engine.SavePhysicalBlob(bytes.NewReader(contentInterm2))
	require.NoError(t, err)

	contentFinal := []byte("permanent final answer data")
	hashFinal, _, err := engine.SavePhysicalBlob(bytes.NewReader(contentFinal))
	require.NoError(t, err)

	exists, err := engine.HasPhysicalBlob(hashInterm1)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = engine.HasPhysicalBlob(hashInterm2)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = engine.HasPhysicalBlob(hashFinal)
	require.NoError(t, err)
	require.True(t, exists)

	taskID := "task-prune-test-101"

	// 1. Initial pending checkpoint
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "pending",
		},
	})
	require.NoError(t, err)

	// 2. Append step 0 checkpoint referencing hashInterm1 via vfs:// URI
	err = engine.SaveIntermediateOutput(taskID, "step-0", map[string]any{
		"file_uri": fmt.Sprintf("vfs://%s", hashInterm1),
	})
	require.NoError(t, err)

	// 3. Append step 1 checkpoint referencing hashInterm2 via protocol.OutputHashKey
	err = engine.SaveIntermediateOutput(taskID, "step-1", map[string]any{
		protocol.OutputHashKey: hashInterm2,
		protocol.OutputNameKey: "interm2.dat",
	})
	require.NoError(t, err)

	// 4. Complete the task referencing hashFinal in final outputs
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
		Outputs: map[string]any{protocol.OutputHashKey: hashFinal},
	})
	require.NoError(t, err)

	// Journal should have 4 records
	journal, err := engine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journal, 4)

	// 5. Prune intermediate task data
	err = engine.PruneIntermediateTaskData(taskID)
	require.NoError(t, err)

	// 6. Verify journal pruned all non-terminal intermediate records
	journalAfter, err := engine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journalAfter, 1, "all intermediate records should be removed from journal")
	require.Equal(t, "completed", journalAfter[0].Status)

	// 7. Verify physical blobs:
	exists, err = engine.HasPhysicalBlob(hashInterm1)
	require.NoError(t, err)
	require.False(t, exists, "intermediate blob 1 should be physically deleted")

	exists, err = engine.HasPhysicalBlob(hashInterm2)
	require.NoError(t, err)
	require.False(t, exists, "intermediate blob 2 should be physically deleted")

	exists, err = engine.HasPhysicalBlob(hashFinal)
	require.NoError(t, err)
	require.True(t, exists, "final result blob must be permanently retained")

	// 8. Verify history record is still intact
	hist, exists, err := engine.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, hashFinal, hist.FinalOutputs[protocol.OutputHashKey])
}

func TestTaskJournalPruningWithPipelineOutputs(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-pipeline-outputs")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	// Verify that when compute engine attaches $pipeline_outputs to final outputs,
	// intermediate blobs are STILL correctly identified and pruned.
	contentInterm := []byte("step-0-transient-blob")
	hashInterm, _, err := engine.SavePhysicalBlob(bytes.NewReader(contentInterm))
	require.NoError(t, err)

	contentFinal := []byte("step-1-final-blob")
	hashFinal, _, err := engine.SavePhysicalBlob(bytes.NewReader(contentFinal))
	require.NoError(t, err)

	taskID := "task-pipeline-outputs-pruning"

	// Intermediate step
	err = engine.SaveIntermediateOutput(taskID, "step-0", map[string]any{
		"transient": fmt.Sprintf("vfs://%s", hashInterm),
	})
	require.NoError(t, err)

	// Completion checkpoint containing $pipeline_outputs as set by compute.go
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
		Outputs: map[string]any{
			"final": fmt.Sprintf("vfs://%s", hashFinal),
			"$pipeline_outputs": map[string]any{
				"step-0": map[string]any{
					"transient": fmt.Sprintf("vfs://%s", hashInterm),
				},
			},
		},
	})
	require.NoError(t, err)

	// Prune
	err = engine.PruneIntermediateTaskData(taskID)
	require.NoError(t, err)

	// Intermediate blob must be pruned despite appearing in $pipeline_outputs
	exists, err := engine.HasPhysicalBlob(hashInterm)
	require.NoError(t, err)
	require.False(t, exists, "intermediate blob must be pruned even if embedded in $pipeline_outputs")

	// Final blob must be retained
	exists, err = engine.HasPhysicalBlob(hashFinal)
	require.NoError(t, err)
	require.True(t, exists, "final result blob must be retained")
}

func TestTaskJournalPruneActiveTaskRejected(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-active-guard")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	taskID := "task-running-active"
	content := []byte("active-data")
	hash, _, err := engine.SavePhysicalBlob(bytes.NewReader(content))
	require.NoError(t, err)

	err = engine.SaveIntermediateOutput(taskID, "step-0", map[string]any{
		"file": fmt.Sprintf("vfs://%s", hash),
	})
	require.NoError(t, err)

	// Attempting to prune an active task must fail
	err = engine.PruneIntermediateTaskData(taskID)
	require.ErrorIs(t, err, storage.ErrTaskActive)

	// Blob must still exist
	exists, err := engine.HasPhysicalBlob(hash)
	require.NoError(t, err)
	require.True(t, exists)

	// Non-existent task must fail with ErrTaskNotFound
	err = engine.PruneIntermediateTaskData("non-existent-task")
	require.ErrorIs(t, err, storage.ErrTaskNotFound)
}

func TestTaskJournalActiveStateInheritanceOnCompletion(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-inheritance")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	taskID := "task-state-inherit-01"

	// Create active task with rich state
	pipeState := &protocol.PipelineExecutionState{
		PipelineID:  "pipeline-alpha",
		CurrentStep: 2,
		Outputs: map[string]map[string]any{
			"step-0": {"a": 1},
			"step-1": {"b": 2},
		},
	}
	err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID:          taskID,
			Status:          "running",
			Service:         "pipeline.service",
			RequesterNodeID: "node-requester",
			ReplyTo:         "node-requester-addr",
			NodeID:          "worker-node-1",
			Payload:         map[string]any{"initial_input": "data"},
			PipelineState:   pipeState,
			Metadata:        map[string]string{"env": "prod", "trace_id": "xyz"},
		},
		StepID:    "step-1",
		StepIndex: 2,
	})
	require.NoError(t, err)

	// Complete task without repeating all fields in terminal checkpoint
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
		StepID:  "step-2",
		Outputs: map[string]any{"final_res": "ok"},
	})
	require.NoError(t, err)

	// Verify all state was inherited into history
	hist, exists, err := engine.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "pipeline.service", hist.Service)
	require.Equal(t, "node-requester", hist.RequesterNodeID)
	require.Equal(t, "node-requester-addr", hist.ReplyTo)
	require.Equal(t, 2, hist.CurrentStep)
	require.Equal(t, "step-2", hist.CurrentStepID)
	require.Equal(t, "worker-node-1", hist.NodeID)
	require.Equal(t, "data", hist.Payload["initial_input"])
	require.Equal(t, "ok", hist.FinalOutputs["final_res"])
	require.NotNil(t, hist.Outputs["step-2"])
	require.Equal(t, "ok", hist.Outputs["step-2"]["final_res"])
	require.Equal(t, "prod", hist.Metadata["env"])
	require.Equal(t, "xyz", hist.Metadata["trace_id"])
	require.NotNil(t, hist.PipelineState)
	require.Equal(t, "pipeline-alpha", hist.PipelineState.PipelineID)
}

func TestTaskJournalTerminalStateCannotRegress(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-regress")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	taskID := "task-regress-guard"

	// Complete the task directly
	err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
	})
	require.NoError(t, err)

	// Attempting to append a non-terminal status must be rejected
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "running",
		},
		StepID: "step-1",
	})
	require.ErrorIs(t, err, storage.ErrTaskTerminal)

	// Must not be present in active tasks
	activeTasks, err := engine.LoadActiveTasks()
	require.NoError(t, err)
	require.NotContains(t, activeTasks, taskID)
}

func TestTaskJournalCrossTaskCASProtection(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-cas-prot")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	// Task A produces intermediate blob that is identical to a blob used by active Task B
	sharedContent := []byte("shared content addressable block")
	sharedHash, _, err := engine.SavePhysicalBlob(bytes.NewReader(sharedContent))
	require.NoError(t, err)

	uniqueContentA := []byte("task A private intermediate")
	uniqueHashA, _, err := engine.SavePhysicalBlob(bytes.NewReader(uniqueContentA))
	require.NoError(t, err)

	finalContentA := []byte("task A final")
	finalHashA, _, err := engine.SavePhysicalBlob(bytes.NewReader(finalContentA))
	require.NoError(t, err)

	// Task B is active and references sharedHash
	err = engine.SaveActiveTask(storage.TaskRecord{
		TaskContext: storage.TaskContext{
			TaskID: "task-B-active",
			Status: "running",
		},
		Outputs: map[string]map[string]any{"step-0": {"blob": fmt.Sprintf("vfs://%s", sharedHash)}},
	})
	require.NoError(t, err)

	// Task A runs with sharedHash and uniqueHashA
	err = engine.SaveIntermediateOutput("task-A", "step-0", map[string]any{
		"shared": fmt.Sprintf("vfs://%s", sharedHash),
		"unique": fmt.Sprintf("vfs://%s", uniqueHashA),
	})
	require.NoError(t, err)

	// Task A completes
	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: "task-A",
			Status: "completed",
		},
		Outputs: map[string]any{"final": fmt.Sprintf("vfs://%s", finalHashA)},
	})
	require.NoError(t, err)

	// Prune Task A intermediate data
	err = engine.PruneIntermediateTaskData("task-A")
	require.NoError(t, err)

	// uniqueHashA should be pruned
	exists, err := engine.HasPhysicalBlob(uniqueHashA)
	require.NoError(t, err)
	require.False(t, exists, "task A private intermediate blob should be pruned")

	// sharedHash MUST NOT be pruned because Task B is actively referencing it
	exists, err = engine.HasPhysicalBlob(sharedHash)
	require.NoError(t, err)
	require.True(t, exists, "shared CAS blob must not be pruned while active task B references it")

	// finalHashA must remain
	exists, err = engine.HasPhysicalBlob(finalHashA)
	require.NoError(t, err)
	require.True(t, exists, "final blob must remain")
}

func TestTaskJournalListAndCleanup(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-list")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	taskID := "task-cleanup-test"

	err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "running",
		},
		StepID: "step-0",
	})
	require.NoError(t, err)

	err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
		TaskContext: storage.TaskContext{
			TaskID: taskID,
			Status: "completed",
		},
		StepID: "step-1",
	})
	require.NoError(t, err)

	histList, err := engine.ListTaskHistory()
	require.NoError(t, err)
	require.Len(t, histList, 1)
	require.Equal(t, taskID, histList[0].TaskID)

	journal, err := engine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Len(t, journal, 2)

	// Delete journal
	err = engine.DeleteTaskJournal(taskID)
	require.NoError(t, err)

	journalAfter, err := engine.GetTaskJournal(taskID)
	require.NoError(t, err)
	require.Empty(t, journalAfter)
}

func TestTaskJournalCrashRecoveryAndPersistence(t *testing.T) {
	t.Parallel()
	storageDir := t.TempDir()

	taskActiveID := "task-recovery-active"
	taskHistID := "task-recovery-completed"

	// Phase 1: Write state using first engine instance
	{
		engine, err := storage.NewStorageEngine(
			nil, storageDir,
			func(protocol.IndexEntry) {},
			func(protocol.IndexEntry, string) error { return nil },
		)
		require.NoError(t, err)

		// Create an active task with intermediate checkpoint
		err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
			TaskContext: storage.TaskContext{
				TaskID: taskActiveID,
				Status: "running",
			},
			StepID:    "step-0",
			StepIndex: 0,
			Outputs:   map[string]any{"data": "retained-after-crash"},
		})
		require.NoError(t, err)

		// Create a completed task
		err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
			TaskContext: storage.TaskContext{
				TaskID: taskHistID,
				Status: "running",
			},
			StepIndex: 0,
		})
		require.NoError(t, err)

		err = engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
			TaskContext: storage.TaskContext{
				TaskID: taskHistID,
				Status: "completed",
			},
			StepIndex: 1,
			Outputs:   map[string]any{"final": "done"},
		})
		require.NoError(t, err)

		// Close cleanly (simulating process restart)
		err = engine.Close()
		require.NoError(t, err)
	}

	// Phase 2: Open second engine instance on the exact same directory
	{
		reopened, err := storage.NewStorageEngine(
			nil, storageDir,
			func(protocol.IndexEntry) {},
			func(protocol.IndexEntry, string) error { return nil },
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = reopened.Close() })

		// 1. Verify Active Tasks recovered
		activeTasks, err := reopened.LoadActiveTasks()
		require.NoError(t, err)
		require.Contains(t, activeTasks, taskActiveID)
		require.NotContains(t, activeTasks, taskHistID)
		active := activeTasks[taskActiveID]
		require.Equal(t, "running", active.Status)
		require.Equal(t, "step-0", active.CurrentStepID)
		require.Equal(t, "retained-after-crash", active.Outputs["step-0"]["data"])

		// 2. Verify History Tasks recovered
		historyTasks, err := reopened.LoadTaskHistory()
		require.NoError(t, err)
		require.Contains(t, historyTasks, taskHistID)
		require.NotContains(t, historyTasks, taskActiveID)
		hist := historyTasks[taskHistID]
		require.Equal(t, "completed", hist.Status)
		require.Equal(t, "done", hist.FinalOutputs["final"])

		// 3. Verify Journal WAL checkpoints recovered
		activeJournal, err := reopened.GetTaskJournal(taskActiveID)
		require.NoError(t, err)
		require.Len(t, activeJournal, 1)
		require.Equal(t, uint64(1), activeJournal[0].Sequence)
		require.Equal(t, "step-0", activeJournal[0].StepID)

		histJournal, err := reopened.GetTaskJournal(taskHistID)
		require.NoError(t, err)
		require.Len(t, histJournal, 2)
		require.Equal(t, uint64(1), histJournal[0].Sequence)
		require.Equal(t, uint64(2), histJournal[1].Sequence)
	}
}

func TestTaskJournalConcurrentCheckpoints(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-storage-journal-concurrent")
	engine := testutil.NewStorageEngine(t, cfg)
	t.Cleanup(func() { _ = engine.Close() })

	numTasks := 10
	numStepsPerTask := 5

	var wg sync.WaitGroup
	for i := range numTasks {
		wg.Add(1)
		go func(taskIdx int) {
			defer wg.Done()
			taskID := fmt.Sprintf("task-conc-%d", taskIdx)
			for s := range numStepsPerTask {
				err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
					TaskContext: storage.TaskContext{
						TaskID: taskID,
						Status: "running",
					},
					StepID:    fmt.Sprintf("step-%d", s),
					StepIndex: s,
					Outputs:   map[string]any{"step": s},
				})
				if err != nil {
					t.Errorf("task %s step %d append checkpoint failed: %v", taskID, s, err)
					return
				}
			}
			// Mark complete
			err := engine.AppendTaskCheckpoint(storage.TaskJournalRecord{
				TaskContext: storage.TaskContext{
					TaskID: taskID,
					Status: "completed",
				},
				Outputs: map[string]any{"success": true},
			})
			if err != nil {
				t.Errorf("task %s completion checkpoint failed: %v", taskID, err)
			}
		}(i)
	}
	wg.Wait()

	// Verify all tasks reached history and have correct sequence numbers
	history, err := engine.LoadTaskHistory()
	require.NoError(t, err)
	require.Len(t, history, numTasks)

	for i := range numTasks {
		taskID := fmt.Sprintf("task-conc-%d", i)
		journal, err := engine.GetTaskJournal(taskID)
		require.NoError(t, err)
		require.Len(t, journal, numStepsPerTask+1)
		for s := range journal {
			require.Equal(t, uint64(s+1), journal[s].Sequence)
		}
	}

	active, err := engine.LoadActiveTasks()
	require.NoError(t, err)
	require.Empty(t, active, "all tasks completed so active bucket should be empty")
}

func TestExtractBlobHashes(t *testing.T) {
	t.Parallel()
	// Test various nested structures
	hash1 := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)
	hash3 := strings.Repeat("c", 64)
	hash4 := strings.Repeat("d", 64)

	input := map[string]any{
		"direct":       fmt.Sprintf("vfs://%s", hash1),
		"nested_map":   map[string]any{protocol.OutputHashKey: hash2},
		"nested_slice": []any{"foo", fmt.Sprintf("vfs://%s", hash3), "bar"},
		"invalid":      "not-a-hash",
		"empty":        "",
	}

	extracted := storage.ExtractBlobHashes(input)
	require.Len(t, extracted, 3)
	require.Contains(t, extracted, hash1)
	require.Contains(t, extracted, hash2)
	require.Contains(t, extracted, hash3)

	nestedTaskOutputs := map[string]map[string]any{
		"step-0": {"blob": fmt.Sprintf("vfs://%s", hash4)},
	}
	extractedNested := storage.ExtractBlobHashes(nestedTaskOutputs)
	require.Len(t, extractedNested, 1)
	require.Contains(t, extractedNested, hash4)
}

func TestTaskContextOverlayAndFillMissing(t *testing.T) {
	t.Parallel()

	// 1. Test Overlay overwriting non-empty fields and copying metadata / payload
	base := storage.TaskContext{
		TaskID:          "task-123",
		Service:         "old-service",
		Status:          "pending",
		RequesterNodeID: "node-orig",
		ReplyTo:         "reply-orig",
		NodeID:          "worker-1",
		Payload:         map[string]any{"in": "val1"},
		Error:           "",
		Metadata:        map[string]string{"k1": "v1"},
	}

	update := storage.TaskContext{
		Service: "new-service",
		Status:  "running",
		Payload: map[string]any{"in": "val2"},
		Error:   "transient error",
		Metadata: map[string]string{"k2": "v2"},
	}

	base.Overlay(update)
	require.Equal(t, "task-123", base.TaskID)
	require.Equal(t, "new-service", base.Service)
	require.Equal(t, "running", base.Status)
	require.Equal(t, "node-orig", base.RequesterNodeID)
	require.Equal(t, "reply-orig", base.ReplyTo)
	require.Equal(t, "worker-1", base.NodeID)
	require.Equal(t, "val2", base.Payload["in"])
	require.Equal(t, "transient error", base.Error)
	require.Equal(t, "v1", base.Metadata["k1"])
	require.Equal(t, "v2", base.Metadata["k2"])

	// 2. Test FillMissing filling in empty fields without overwriting existing
	target := storage.TaskContext{
		TaskID:   "task-123",
		Status:   "completed",
		Error:    "",
		Metadata: map[string]string{"k2": "target_v2"},
	}

	target.FillMissing(base)
	require.Equal(t, "task-123", target.TaskID)
	require.Equal(t, "completed", target.Status, "Status must remain completed")
	require.Equal(t, "new-service", target.Service, "Empty service should be filled")
	require.Equal(t, "node-orig", target.RequesterNodeID)
	require.Equal(t, "reply-orig", target.ReplyTo)
	require.Equal(t, "worker-1", target.NodeID)
	require.Equal(t, "val2", target.Payload["in"])
	require.Equal(t, "target_v2", target.Metadata["k2"], "Existing metadata key should not be overwritten")
	require.Equal(t, "v1", target.Metadata["k1"], "Missing metadata key should be added")
}

func TestTaskRecordBlobHashes(t *testing.T) {
	t.Parallel()
	hash1 := strings.Repeat("1", 64)
	hash2 := strings.Repeat("2", 64)
	hash3 := strings.Repeat("3", 64)

	record := storage.TaskRecord{
		TaskContext: storage.TaskContext{
			TaskID:  "task-blob-test",
			Payload: map[string]any{"file": fmt.Sprintf("vfs://%s", hash1)},
		},
		Outputs: map[string]map[string]any{
			"step-0": {"blob": fmt.Sprintf("vfs://%s", hash2)},
		},
		FinalOutputs: map[string]any{
			"result": fmt.Sprintf("vfs://%s", hash3),
		},
	}

	hashes := record.BlobHashes()
	require.Len(t, hashes, 3)
	require.Contains(t, hashes, hash1)
	require.Contains(t, hashes, hash2)
	require.Contains(t, hashes, hash3)
}
