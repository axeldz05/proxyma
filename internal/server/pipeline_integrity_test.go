package server_test

import (
	"context"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPipelineGossipCannotDowngradeNewerSchema(t *testing.T) {
	t.Parallel()

	source := NewServer(t, testutil.DefaultConfig(t, "schema-source"), nil)
	target := NewServer(t, testutil.DefaultConfig(t, "schema-target"), nil)
	linkClusterPeers(t, source, target)

	newer := protocol.PipelineSchema{
		ID:      "versioned-pipeline",
		Version: 2,
		Steps:   []protocol.PipelineStep{{ID: "new-step", Service: "new-service"}},
	}
	require.NoError(t, target.LocalPipelineAdd(string(mustMarshal(newer))))

	older := protocol.PipelineSchema{
		ID:      newer.ID,
		Version: 1,
		Steps:   []protocol.PipelineStep{{ID: "old-step", Service: "old-service"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := source.PeerClient().NotifyPipelineSchema(ctx, target.Config.ID, protocol.PipelineNotification{
		NodeID: source.Config.ID,
		Schema: older,
		Action: protocol.ActionAdd,
	})
	require.Error(t, err)

	got, ok := target.Compute.GetPipeline(newer.ID)
	require.True(t, ok)
	require.Equal(t, newer.Version, got.Version)
	require.Equal(t, protocol.PipelineSchemaHash(newer), protocol.PipelineSchemaHash(got))

	persisted, err := target.Storage.LoadPipelineSchemas()
	require.NoError(t, err)
	require.Equal(t, protocol.PipelineSchemaHash(newer), protocol.PipelineSchemaHash(persisted[newer.ID]))
}

func TestPipelineTombstonePersistsAndRejectsDelayedResurrection(t *testing.T) {
	t.Parallel()

	node := NewServer(t, testutil.DefaultConfig(t, "tombstone-node"), nil)
	schema := protocol.PipelineSchema{
		ID:      "removed-pipeline",
		Version: 4,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "service"}},
	}
	require.NoError(t, node.LocalPipelineAdd(string(mustMarshal(schema))))
	require.NoError(t, node.LocalPipelineRemove(schema.ID))

	_, active := node.Compute.GetPipeline(schema.ID)
	require.False(t, active)
	revision, exists := node.Compute.GetPipelineRevision(schema.ID)
	require.True(t, exists)
	require.True(t, revision.Deleted)
	require.Equal(t, schema.Version, revision.Version)

	persisted, err := node.Storage.LoadPipelineSchemas()
	require.NoError(t, err)
	require.True(t, persisted[schema.ID].Deleted)
	require.Equal(t, schema.Version, persisted[schema.ID].Version)

	require.Error(t, node.LocalPipelineAdd(string(mustMarshal(schema))), "equal revision must not resurrect")
	older := schema
	older.Version--
	require.Error(t, node.LocalPipelineAdd(string(mustMarshal(older))), "older revision must not resurrect")
	_, active = node.Compute.GetPipeline(schema.ID)
	require.False(t, active)

	newer := schema
	newer.Version++
	require.NoError(t, node.LocalPipelineAdd(string(mustMarshal(newer))))
	got, active := node.Compute.GetPipeline(schema.ID)
	require.True(t, active)
	require.Equal(t, newer.Version, got.Version)
}

func TestDispatchTaskBindsPipelineRevisionBeforeRemoteSubmit(t *testing.T) {
	t.Parallel()

	submitted := make(chan protocol.TaskRequest, 1)
	client := &testutil.MockPeerClient{
		OnSubmitTask: func(_ context.Context, _ string, request protocol.TaskRequest) error {
			submitted <- request
			return nil
		},
	}
	node := NewServer(t, testutil.DefaultConfig(t, "pipeline-dispatcher"), client)
	node.AddPeer("pipeline-worker", protocol.AddressRecord{Addresses: []string{"https://worker.invalid"}})
	node.SetPeerOnline("pipeline-worker", true)

	schema := protocol.PipelineSchema{
		ID:      "bound-before-dispatch",
		Version: 7,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "worker-service"}},
	}
	require.NoError(t, node.Compute.RegisterPipeline(schema))
	require.NoError(t, node.DispatchTask("pipeline-worker", protocol.TaskRequest{
		TaskID:  "bound-dispatch",
		Service: schema.ID,
		Payload: map[string]any{},
	}))

	select {
	case request := <-submitted:
		require.NotNil(t, request.PipelineState)
		require.Equal(t, schema.ID, request.PipelineState.PipelineID)
		require.Equal(t, schema.Version, request.PipelineState.PipelineVersion)
		require.Equal(t, protocol.PipelineSchemaHash(schema), request.PipelineState.SchemaHash)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote task submission")
	}
}

func TestPipelineFinalOutputFetchedFromActualProducer(t *testing.T) {
	t.Parallel()

	requester := NewServer(t, testutil.DefaultConfig(t, "pipeline-requester"), nil)
	middle := NewServer(t, testutil.DefaultConfig(t, "pipeline-middle"), nil)
	producer := NewServer(t, testutil.DefaultConfig(t, "pipeline-producer"), nil)
	linkClusterPeers(t, requester, middle, producer)

	require.NoError(t, middle.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:       "pipeline/pass",
		Parameters: map[string]protocol.ServiceParameter{"value": {Type: protocol.ParamTypeString, Required: true}},
		Outputs:    map[string]protocol.ServiceParameter{"value": {Type: protocol.ParamTypeString}},
	}, compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{"value": payload["value"]}, nil
	})))

	outputBytes := []byte("blob-created-on-final-pipeline-node")
	outputPath := filepath.Join(t.TempDir(), "pipeline-output.txt")
	require.NoError(t, os.WriteFile(outputPath, outputBytes, 0o600))
	require.NoError(t, producer.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:       "pipeline/write",
		Parameters: map[string]protocol.ServiceParameter{"value": {Type: protocol.ParamTypeString, Required: true}},
		Outputs:    map[string]protocol.ServiceParameter{"file": {Type: protocol.ParamTypeFile}},
	}, compute.BuildUnaryHandler(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"file": outputPath}, nil
	})))

	schema := protocol.PipelineSchema{
		ID:      "remote-final-output",
		Version: 3,
		Steps: []protocol.PipelineStep{
			{ID: "pass", Service: "pipeline/pass", TargetNodeID: middle.Config.ID},
			{ID: "write", Service: "pipeline/write", TargetNodeID: producer.Config.ID},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "$initial", FromPort: "value", ToStep: "pass", ToPort: "value"},
			{FromStep: "pass", FromPort: "value", ToStep: "write", ToPort: "value"},
		},
	}
	require.NoError(t, requester.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		middleSchema, middleOK := middle.Compute.GetPipeline(schema.ID)
		producerSchema, producerOK := producer.Compute.GetPipeline(schema.ID)
		return middleOK && producerOK &&
			protocol.PipelineSchemaHash(middleSchema) == protocol.PipelineSchemaHash(schema) &&
			protocol.PipelineSchemaHash(producerSchema) == protocol.PipelineSchemaHash(schema)
	}, 3*time.Second, 25*time.Millisecond)

	response, err := requester.LocalServiceRun(schema.ID, `{"value":"input"}`)
	require.NoError(t, err)
	require.Equal(t, "completed", response.Status)
	require.Equal(t, producer.Config.ID, response.ProducerNodeID)

	localPath := protocol.ResultLocalPath(response.Outputs)
	require.NotEmpty(t, localPath)
	got, err := os.ReadFile(localPath)
	require.NoError(t, err)
	require.Equal(t, outputBytes, got)
}

func TestPipelineIntermediateBlobFetchedFromProducingStep(t *testing.T) {
	t.Parallel()

	requester := NewServer(t, testutil.DefaultConfig(t, "provenance-requester"), nil)
	producer := NewServer(t, testutil.DefaultConfig(t, "provenance-producer"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "provenance-consumer"), nil)
	linkClusterPeers(t, requester, producer, consumer)

	content := []byte("intermediate-produced-remotely")
	outputPath := filepath.Join(t.TempDir(), "intermediate.txt")
	require.NoError(t, os.WriteFile(outputPath, content, 0o600))
	require.NoError(t, producer.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:    "pipeline/create-intermediate",
		Outputs: map[string]protocol.ServiceParameter{"file": {Type: protocol.ParamTypeFile}},
	}, compute.BuildUnaryHandler(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"file": outputPath}, nil
	})))

	require.NoError(t, consumer.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:       "pipeline/read-intermediate",
		Parameters: map[string]protocol.ServiceParameter{"file": {Type: protocol.ParamTypeFile, Required: true}},
	}, compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
		path, _ := payload["file"].(string)
		got, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": string(got)}, nil
	})))

	schema := protocol.PipelineSchema{
		ID:      "intermediate-provenance",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "produce", Service: "pipeline/create-intermediate", TargetNodeID: producer.Config.ID},
			{ID: "consume", Service: "pipeline/read-intermediate", TargetNodeID: consumer.Config.ID},
		},
		Connections: []protocol.PipelineConnection{
			{FromStep: "produce", FromPort: "file", ToStep: "consume", ToPort: "file"},
		},
	}
	require.NoError(t, requester.LocalPipelineAdd(string(mustMarshal(schema))))
	require.Eventually(t, func() bool {
		_, producerOK := producer.Compute.GetPipeline(schema.ID)
		_, consumerOK := consumer.Compute.GetPipeline(schema.ID)
		return producerOK && consumerOK
	}, 3*time.Second, 25*time.Millisecond)

	response, err := requester.LocalServiceRun(schema.ID, `{}`)
	require.NoError(t, err)
	require.Equal(t, "completed", response.Status)
	require.Equal(t, string(content), response.Outputs["content"])
}

func TestRemoteOutputIngestFailureUpdatesStoredTerminalStatus(t *testing.T) {
	t.Parallel()

	requester := NewServer(t, testutil.DefaultConfig(t, "missing-output-requester"), nil)
	producer := NewServer(t, testutil.DefaultConfig(t, "missing-output-producer"), nil)
	linkClusterPeers(t, requester, producer)

	missingHash := strings.Repeat("a", 64)
	require.NoError(t, producer.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "missing-output",
	}, compute.BuildUnaryHandler(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{
			protocol.OutputHashKey: missingHash,
			protocol.OutputNameKey: "missing.bin",
			protocol.OutputSizeKey: float64(8),
			"file":                 protocol.VFSURI(missingHash),
		}, nil
	})))

	response, err := requester.LocalServiceRun("missing-output", `{}`)
	require.Error(t, err)
	require.Equal(t, "failed", response.Status)
	require.Contains(t, response.Error, "output blob download failed")

	stored, ok := requester.Compute.GetTaskResponse(response.TaskID)
	require.True(t, ok)
	require.Equal(t, "failed", stored.Status)
	require.Equal(t, response.Error, stored.Error)
}

func TestLocalOutputSaveFailureCannotRemainCompleted(t *testing.T) {
	t.Parallel()

	node := NewServer(t, testutil.DefaultConfig(t, "local-save-failure"), nil)
	node.Compute.SetVFSBlobStager(nil)
	unreadableOutput := t.TempDir()
	hash := strings.Repeat("b", 64)

	require.NoError(t, node.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "bad-local-output",
	}, compute.BuildUnaryHandler(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{
			protocol.OutputHashKey:      hash,
			protocol.OutputNameKey:      "bad-output.bin",
			protocol.OutputSizeKey:      float64(1),
			protocol.ResultLocalPathKey: unreadableOutput,
		}, nil
	})))

	response, err := node.LocalServiceRun("bad-local-output", `{}`)
	require.Error(t, err)
	require.Equal(t, "failed", response.Status)
	require.Contains(t, response.Error, "save local output blob")

	stored, ok := node.Compute.GetTaskResponse(response.TaskID)
	require.True(t, ok)
	require.Equal(t, "failed", stored.Status)
	require.Equal(t, response.Error, stored.Error)
}
