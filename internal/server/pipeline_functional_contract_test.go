package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"proxyma/shared/uischema"

	"github.com/stretchr/testify/require"
)

func publicTaskStatus(t *testing.T, node *TestServer, taskID string) protocol.ServiceTaskResponse {
	t.Helper()
	raw, err := server.CallUnixUnary(
		node.Server,
		uischema.MustUnixAction("service", "status"),
		map[string]string{"task_id": taskID},
	)
	require.NoError(t, err)
	status, ok := raw.(protocol.ServiceTaskResponse)
	require.True(t, ok, "task status type = %T", raw)
	return status
}

func TestDistributedPipelineIntermediateFailureIsTerminal(t *testing.T) {
	t.Parallel()

	requester := NewServer(t, testutil.DefaultConfig(t, "pipeline-failure-requester"), nil)
	firstProvider := NewServer(t, testutil.DefaultConfig(t, "pipeline-failure-first"), nil)
	failingProvider := NewServer(t, testutil.DefaultConfig(t, "pipeline-failure-second"), nil)
	linkClusterPeers(t, requester, firstProvider, failingProvider)

	require.NoError(t, firstProvider.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "pipeline/pass",
			Parameters: map[string]protocol.ServiceParameter{
				"value": {Type: protocol.ParamTypeInt, Required: true},
			},
			Outputs: map[string]protocol.ServiceParameter{
				"value": {Type: protocol.ParamTypeInt},
			},
		},
		compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{"value": payload["value"]}, nil
		}),
	))
	require.NoError(t, failingProvider.Compute.RegisterNewService(
		protocol.ServiceSchema{
			Name: "pipeline/fail",
			Parameters: map[string]protocol.ServiceParameter{
				"value": {Type: protocol.ParamTypeInt, Required: true},
			},
		},
		compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
			return nil, errors.New("remote pipeline step failed")
		}),
	))

	schema := protocol.PipelineSchema{
		ID:      "distributed-failure",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "pass", Service: "pipeline/pass", TargetNodeID: firstProvider.Config.ID},
			{ID: "fail", Service: "pipeline/fail", TargetNodeID: failingProvider.Config.ID},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "$initial", FromPort: "seed", ToStep: "pass", ToPort: "value"},
			{FromStep: "pass", FromPort: "value", ToStep: "fail", ToPort: "value"},
		},
	}
	require.NoError(t, requester.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, firstOK := firstProvider.Compute.GetPipeline(schema.ID)
		_, failingOK := failingProvider.Compute.GetPipeline(schema.ID)
		return firstOK && failingOK
	}, 3*time.Second, 25*time.Millisecond)

	response, err := requester.LocalServiceRun(schema.ID, `{"seed":7}`)
	require.ErrorContains(t, err, "remote pipeline step failed")
	require.Equal(t, "failed", response.Status)
	require.Contains(t, response.Error, "remote pipeline step failed")

	status := publicTaskStatus(t, requester, response.TaskID)
	require.Equal(t, "failed", status.Status)
	require.Equal(t, response.Error, status.Error)
}

func TestDistributedPipelineSelectsDynamicProviderFullStack(t *testing.T) {
	t.Parallel()

	requester := NewServer(t, testutil.DefaultConfig(t, "pipeline-dynamic-requester"), nil)
	providerA := NewServer(t, testutil.DefaultConfig(t, "pipeline-dynamic-a"), nil)
	providerB := NewServer(t, testutil.DefaultConfig(t, "pipeline-dynamic-b"), nil)
	linkClusterPeers(t, requester, providerA, providerB)

	registerProvider := func(node *TestServer) {
		nodeID := node.Config.ID
		require.NoError(t, node.Compute.RegisterNewService(
			protocol.ServiceSchema{
				Name: "pipeline/dynamic-provider",
				Outputs: map[string]protocol.ServiceParameter{
					"provider": {Type: protocol.ParamTypeString},
				},
			},
			compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
				return map[string]any{"provider": nodeID}, nil
			}),
		))
	}
	registerProvider(providerA)
	registerProvider(providerB)

	schema := protocol.PipelineSchema{
		ID:      "dynamic-provider-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "dynamic", Service: "pipeline/dynamic-provider"},
		},
	}
	require.NoError(t, requester.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, aOK := providerA.Compute.GetPipeline(schema.ID)
		_, bOK := providerB.Compute.GetPipeline(schema.ID)
		return aOK && bOK
	}, 3*time.Second, 25*time.Millisecond)

	response, err := requester.LocalServiceRun(schema.ID, `{}`)
	require.NoError(t, err)
	require.Equal(t, "completed", response.Status)
	require.Contains(
		t,
		[]string{providerA.Config.ID, providerB.Config.ID},
		response.Outputs["provider"],
	)

	pipelineOutputs, ok := response.Outputs["$pipeline_outputs"].(map[string]any)
	require.True(t, ok)
	stepOutputs, ok := pipelineOutputs["dynamic"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, response.Outputs["provider"], stepOutputs["provider"])
	require.Equal(t, "completed", publicTaskStatus(t, requester, response.TaskID).Status)
}
