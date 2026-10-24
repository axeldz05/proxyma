package server_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func TestPipelineEscrow_IntermediateWorkerEscalatesToCoordinator(t *testing.T) {
	t.Parallel()

	nodeA := NewServer(t, testutil.DefaultConfig(t, "node-coord-a"), nil)
	nodeB := NewServer(t, testutil.DefaultConfig(t, "node-worker-b"), nil)
	linkClusterPeers(t, nodeA, nodeB)

	// Node B provides step 0
	require.NoError(t, nodeB.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "escrow/step0",
			Parameters: map[string]protocol.ServiceParameter{
				"in_data": {Type: protocol.ParamTypeString},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"inter_data": "step0_result"}, nil
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "escrow-test-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "escrow/step0", TargetNodeID: nodeB.Config.ID},
			{ID: "step1", Service: "escrow/unsupported-step1"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "inter_data", ToStep: "step1", ToPort: "step1_in"},
		},
	}
	require.NoError(t, nodeA.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, ok := nodeB.Compute.GetPipeline(schema.ID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	taskID := "task-escrow-p2p-1"
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         schema.ID,
		RequesterNodeID: nodeA.Config.ID,
		ReplyTo:         protocol.PeerHTTPSURL(nodeA.Config.ID, protocol.PathServicesCallback),
		Payload:         map[string]any{"in_data": "start"},
	}
	require.NoError(t, nodeA.Compute.BindPipelineTask(&taskReq))

	// Dispatch task from coordinator Node A to worker Node B
	require.NoError(t, nodeA.DispatchTask(nodeB.Config.ID, taskReq))

	// Node B should execute step 0, discover no peer for step 1, and escalate to Node A escrow
	require.Eventually(t, func() bool {
		escrowReq, inEscrow := nodeA.Compute.GetEscrowTask(taskID)
		return inEscrow && escrowReq.StepID == "step1" && escrowReq.StepIndex == 1
	}, 5*time.Second, 50*time.Millisecond)

	// Verify task in Node A's bucketTasksActive has status escrow
	activeTask, exists, err := nodeA.Storage.GetActiveTask(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, protocol.TaskStatusEscrow, activeTask.Status)
	require.Equal(t, 1, activeTask.CurrentStep)
	require.Equal(t, "step1", activeTask.CurrentStepID)
	require.Equal(t, "step0_result", activeTask.Outputs["step0"]["inter_data"])
}

func TestPipelineEscrow_MeshAnnouncementWakeupAndCompletion(t *testing.T) {
	t.Parallel()

	nodeA := NewServer(t, testutil.DefaultConfig(t, "node-coord-mesh-a"), nil)
	nodeB := NewServer(t, testutil.DefaultConfig(t, "node-worker-mesh-b"), nil)
	linkClusterPeers(t, nodeA, nodeB)

	// Node B provides step 0
	require.NoError(t, nodeB.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "mesh/step0",
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"msg": "data-from-b"}, nil
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "mesh-resume-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "mesh/step0", TargetNodeID: nodeB.Config.ID},
			{ID: "step1", Service: "mesh/late-step1"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "msg", ToStep: "step1", ToPort: "in"},
		},
	}
	require.NoError(t, nodeA.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, ok := nodeB.Compute.GetPipeline(schema.ID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	taskID := "task-mesh-resume-1"
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         schema.ID,
		RequesterNodeID: nodeA.Config.ID,
		ReplyTo:         protocol.PeerHTTPSURL(nodeA.Config.ID, protocol.PathServicesCallback),
		Payload:         map[string]any{},
	}
	require.NoError(t, nodeA.Compute.BindPipelineTask(&taskReq))

	// Dispatch task to Node B
	require.NoError(t, nodeA.DispatchTask(nodeB.Config.ID, taskReq))

	// Wait for Node A to hold the task in escrow
	require.Eventually(t, func() bool {
		_, inEscrow := nodeA.Compute.GetEscrowTask(taskID)
		return inEscrow
	}, 5*time.Second, 50*time.Millisecond)

	// Now start Node C which provides the missing step 1 service
	nodeC := NewServer(t, testutil.DefaultConfig(t, "node-late-worker-c"), nil)
	require.NoError(t, nodeC.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "mesh/late-step1",
			Parameters: map[string]protocol.ServiceParameter{
				"in": {Type: protocol.ParamTypeString},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"final_answer": payload["in"].(string) + "+completed-by-c"}, nil
		}),
	))
	require.NoError(t, nodeC.LocalPipelineAdd(string(mustMarshal(schema))))

	// Node C joins cluster by announcing to Node A
	linkClusterPeers(t, nodeA, nodeC)
	linkClusterPeers(t, nodeB, nodeC)

	// Node A should detect Node C providing mesh/late-step1, wake up task from escrow, and dispatch to Node C
	require.Eventually(t, func() bool {
		hist, exists, _ := nodeA.Storage.GetTaskHistory(taskID)
		return exists && hist.Status == "completed"
	}, 10*time.Second, 100*time.Millisecond)

	hist, exists, err := nodeA.Storage.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "data-from-b+completed-by-c", hist.FinalOutputs["final_answer"])
}

func TestPipelineOutputAtomicity_PruningIntermediatePreservesFinalVFSOutput(t *testing.T) {
	t.Parallel()

	node := NewServer(t, testutil.DefaultConfig(t, "atomicity-node"), nil)

	// Service 0 creates a temporary file and returns it as output
	var interBlobHash string
	require.NoError(t, node.Compute.RegisterNewService(
		protocol.ServiceSchema{Name: "atomicity/step0"},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			tmpFile := filepath.Join(t.TempDir(), "inter.tmp")
			require.NoError(t, os.WriteFile(tmpFile, []byte("temporary step0 data"), 0644))
			h, _, err := node.Storage.SavePhysicalBlob(bytes.NewReader([]byte("temporary step0 data")))
			require.NoError(t, err)
			interBlobHash = h
			return map[string]any{
				"file": tmpFile,
			}, nil
		}),
	))

	// Service 1 creates the final output file
	var finalBlobHash string
	require.NoError(t, node.Compute.RegisterNewService(
		protocol.ServiceSchema{Name: "atomicity/step1"},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			finalFile := filepath.Join(t.TempDir(), "final.pdf")
			require.NoError(t, os.WriteFile(finalFile, []byte("final permanent document content"), 0644))
			h, _, err := node.Storage.SavePhysicalBlob(bytes.NewReader([]byte("final permanent document content")))
			require.NoError(t, err)
			finalBlobHash = h
			return map[string]any{
				"result_file": finalFile,
			}, nil
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "atomicity-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "atomicity/step0"},
			{ID: "step1", Service: "atomicity/step1"},
		},
	}
	require.NoError(t, node.LocalPipelineAdd(string(mustMarshal(schema))))

	// Run pipeline
	resp, err := node.LocalServiceRun(schema.ID, `{}`)
	require.NoError(t, err)
	require.Equal(t, "completed", resp.Status)

	// 1. Final blob must exist in VFS physical storage
	hasFinal, err := node.Storage.HasPhysicalBlob(finalBlobHash)
	require.NoError(t, err)
	require.True(t, hasFinal, "final blob must be permanently preserved in physical storage")

	// 2. Intermediate data pruning check: intermediate blob must be automatically pruned upon completion
	hasInter, err := node.Storage.HasPhysicalBlob(interBlobHash)
	require.NoError(t, err)
	require.NotEmpty(t, interBlobHash)
	require.False(t, hasInter, "intermediate blob must be automatically pruned upon completion")

	// 3. Final blob STILL exists and survives intermediate pruning
	hasFinalAfterPrune, err := node.Storage.HasPhysicalBlob(finalBlobHash)
	require.NoError(t, err)
	require.True(t, hasFinalAfterPrune, "final blob must survive intermediate pruning")

	// 4. Terminal task history remains intact
	hist, exists, err := node.Storage.GetTaskHistory(resp.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
}

func TestPipelineEscrow_MultiHopEscrowAcrossMultipleSteps(t *testing.T) {
	t.Parallel()

	nodeA := NewServer(t, testutil.DefaultConfig(t, "node-multihop-a"), nil)
	nodeB := NewServer(t, testutil.DefaultConfig(t, "node-multihop-b"), nil)
	linkClusterPeers(t, nodeA, nodeB)

	// Node B provides step 0
	require.NoError(t, nodeB.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "multihop/step0",
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"v0": "b_data"}, nil
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "multihop-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "multihop/step0", TargetNodeID: nodeB.Config.ID},
			{ID: "step1", Service: "multihop/step1"},
			{ID: "step2", Service: "multihop/step2"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "v0", ToStep: "step1", ToPort: "v1_in"},
			{FromStep: "step1", FromPort: "v1_out", ToStep: "step2", ToPort: "v2_in"},
		},
	}
	require.NoError(t, nodeA.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, ok := nodeB.Compute.GetPipeline(schema.ID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	taskID := "task-multihop-escrow-1"
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         schema.ID,
		RequesterNodeID: nodeA.Config.ID,
		ReplyTo:         protocol.PeerHTTPSURL(nodeA.Config.ID, protocol.PathServicesCallback),
		Payload:         map[string]any{},
	}
	require.NoError(t, nodeA.Compute.BindPipelineTask(&taskReq))

	// Dispatch task to Node B
	require.NoError(t, nodeA.DispatchTask(nodeB.Config.ID, taskReq))

	// Hop 1 Escrow: Node B finishes step 0, cannot find candidate for step 1, escalates to Node A
	require.Eventually(t, func() bool {
		escrowReq, inEscrow := nodeA.Compute.GetEscrowTask(taskID)
		return inEscrow && escrowReq.StepID == "step1" && escrowReq.StepIndex == 1
	}, 5*time.Second, 50*time.Millisecond)

	// Now start Node C providing step 1 (but not step 2)
	nodeC := NewServer(t, testutil.DefaultConfig(t, "node-multihop-c"), nil)
	require.NoError(t, nodeC.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "multihop/step1",
			Parameters: map[string]protocol.ServiceParameter{
				"v1_in": {Type: protocol.ParamTypeString},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"v1_out": payload["v1_in"].(string) + "+c_data"}, nil
		}),
	))
	require.NoError(t, nodeC.LocalPipelineAdd(string(mustMarshal(schema))))

	linkClusterPeers(t, nodeA, nodeC)
	linkClusterPeers(t, nodeB, nodeC)

	// Hop 2 Escrow: Node A resumes step 1 on Node C. Node C completes step 1,
	// but cannot find candidate for step 2, so Node C escalates back to Node A!
	require.Eventually(t, func() bool {
		escrowReq, inEscrow := nodeA.Compute.GetEscrowTask(taskID)
		return inEscrow && escrowReq.StepID == "step2" && escrowReq.StepIndex == 2
	}, 10*time.Second, 100*time.Millisecond)

	// Now start Node D providing step 2
	nodeD := NewServer(t, testutil.DefaultConfig(t, "node-multihop-d"), nil)
	require.NoError(t, nodeD.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "multihop/step2",
			Parameters: map[string]protocol.ServiceParameter{
				"v2_in": {Type: protocol.ParamTypeString},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"final_answer": payload["v2_in"].(string) + "+d_data"}, nil
		}),
	))
	require.NoError(t, nodeD.LocalPipelineAdd(string(mustMarshal(schema))))

	linkClusterPeers(t, nodeA, nodeD)
	linkClusterPeers(t, nodeC, nodeD)

	// Node A should wake up task from escrow for step 2, dispatch to Node D, and complete
	require.Eventually(t, func() bool {
		hist, exists, _ := nodeA.Storage.GetTaskHistory(taskID)
		return exists && hist.Status == "completed"
	}, 10*time.Second, 100*time.Millisecond)

	hist, exists, err := nodeA.Storage.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "b_data+c_data+d_data", hist.FinalOutputs["final_answer"])
}

func TestPipelineEscrow_CoordinatorRestartRecoveryWithLateWorker(t *testing.T) {
	t.Parallel()

	cfgA := testutil.DefaultConfig(t, "node-restart-coord-a")
	nodeA := NewServer(t, cfgA, nil)
	nodeB := NewServer(t, testutil.DefaultConfig(t, "node-restart-worker-b"), nil)
	linkClusterPeers(t, nodeA, nodeB)

	// Node B provides step 0
	require.NoError(t, nodeB.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "restart/step0",
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"init": "restart_init_val"}, nil
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "restart-escrow-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "step0", Service: "restart/step0", TargetNodeID: nodeB.Config.ID},
			{ID: "step1", Service: "restart/late-step1"},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "step0", FromPort: "init", ToStep: "step1", ToPort: "in_val"},
		},
	}
	require.NoError(t, nodeA.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, ok := nodeB.Compute.GetPipeline(schema.ID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	taskID := "task-restart-escrow-1"
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         schema.ID,
		RequesterNodeID: nodeA.Config.ID,
		ReplyTo:         protocol.PeerHTTPSURL(nodeA.Config.ID, protocol.PathServicesCallback),
		Payload:         map[string]any{},
	}
	require.NoError(t, nodeA.Compute.BindPipelineTask(&taskReq))

	// Dispatch task to Node B
	require.NoError(t, nodeA.DispatchTask(nodeB.Config.ID, taskReq))

	// Wait for Node A to hold the task in escrow
	require.Eventually(t, func() bool {
		escrowReq, inEscrow := nodeA.Compute.GetEscrowTask(taskID)
		return inEscrow && escrowReq.StepID == "step1"
	}, 5*time.Second, 50*time.Millisecond)

	// Now simulate hard coordinator daemon restart:
	// Shutdown Node A completely
	nodeA.httpTestSrv.CloseClientConnections()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	require.NoError(t, nodeA.Shutdown(shutdownCtx))
	cancelShutdown()
	nodeA.httpTestSrv.Close()

	// Restart Node A from same persistent storage
	nodeARestarted := NewServer(t, cfgA, nil)

	// Verify task was durably recovered from bbolt active bucket into memory escrow
	escrowReq, inEscrow := nodeARestarted.Compute.GetEscrowTask(taskID)
	require.True(t, inEscrow, "escrow task must be recovered on daemon boot")
	require.Equal(t, "step1", escrowReq.StepID)
	require.Equal(t, 1, escrowReq.StepIndex)

	taskStatus, exists := nodeARestarted.Compute.GetTaskResponse(taskID)
	require.True(t, exists, "task status must be restored on coordinator boot")
	require.Equal(t, protocol.TaskStatusEscrow, taskStatus.Status)

	// Now bring up Node C providing the missing service
	nodeC := NewServer(t, testutil.DefaultConfig(t, "node-restart-worker-c"), nil)
	require.NoError(t, nodeC.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "restart/late-step1",
			Parameters: map[string]protocol.ServiceParameter{
				"in_val": {Type: protocol.ParamTypeString},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"final_val": payload["in_val"].(string) + "+recovered_ok"}, nil
		}),
	))
	require.NoError(t, nodeC.LocalPipelineAdd(string(mustMarshal(schema))))

	// Node C joins mesh with restarted Node A
	linkClusterPeers(t, nodeARestarted, nodeC)
	linkClusterPeers(t, nodeB, nodeC)

	// Node A should wake up the restored escrow task and complete the pipeline
	require.Eventually(t, func() bool {
		hist, exists, _ := nodeARestarted.Storage.GetTaskHistory(taskID)
		return exists && hist.Status == "completed"
	}, 10*time.Second, 100*time.Millisecond)

	hist, exists, err := nodeARestarted.Storage.GetTaskHistory(taskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "completed", hist.Status)
	require.Equal(t, "restart_init_val+recovered_ok", hist.FinalOutputs["final_val"])
}

