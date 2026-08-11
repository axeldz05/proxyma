package server_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func linkClusterPeers(t *testing.T, nodes ...*TestServer) {
	t.Helper()
	for _, a := range nodes {
		a.SetAddress(a.httpTestSrv.URL)
	}
	for _, a := range nodes {
		for _, b := range nodes {
			if a.Config.ID == b.Config.ID {
				continue
			}
			rec := protocol.AddressRecord{Addresses: []string{b.httpTestSrv.URL}}
			a.PeerClient().UpdatePeerRoute(b.Config.ID, rec)
			a.AddPeer(b.Config.ID, rec)
			a.SetPeerOnline(b.Config.ID, true)
		}
	}
}

func TestDispatchTaskStagesLocalFileToVFSURI(t *testing.T) {
	t.Parallel()

	provider := NewServer(t, testutil.DefaultConfig(t, "stage-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "stage-consumer"), nil)
	linkClusterPeers(t, provider, consumer)

	var seenPayload map[string]any
	require.NoError(t, provider.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "echo-file",
		Parameters: map[string]protocol.ServiceParameter{
			"file": {Type: protocol.ParamTypeFile, Required: true},
		},
	}, compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
		seenPayload = payload
		return map[string]any{"status": "ok"}, nil
	})))

	localFile := filepath.Join(t.TempDir(), "input.txt")
	require.NoError(t, os.WriteFile(localFile, []byte("stage-me"), 0o644))

	taskID := "stage-task-1"
	err := consumer.DispatchTask(provider.Config.ID, protocol.TaskRequest{
		TaskID:  taskID,
		Service: "echo-file",
		Payload: map[string]any{"file": localFile},
		ReplyTo: consumer.Config.Address + protocol.PathServicesCallback,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		r, ok := provider.Compute.GetTaskResponse(taskID)
		return ok && r.Status == "completed"
	}, 3*time.Second, 50*time.Millisecond)

	require.NotNil(t, seenPayload)
	fileVal, ok := seenPayload["file"].(string)
	require.True(t, ok)
	// DispatchTask rewrites to vfs://; provider vfsBlobResolver may resolve to local CAS path.
	hash := ""
	if protocol.IsVFSURI(fileVal) {
		hash, ok = protocol.ParseVFSURI(fileVal)
		require.True(t, ok)
	} else {
		hash = filepath.Base(fileVal)
		require.Len(t, hash, 64, "expected CAS hash path after VFS resolve, got %q", fileVal)
	}
	blobPath := consumer.Storage.GetBlobPath(hash)
	data, err := os.ReadFile(blobPath)
	require.NoError(t, err)
	require.Equal(t, "stage-me", string(data))
}

func TestLocalServiceRunAutoFetchesRemoteOutputBlob(t *testing.T) {
	t.Parallel()

	provider := NewServer(t, testutil.DefaultConfig(t, "fetch-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "fetch-consumer"), nil)
	linkClusterPeers(t, provider, consumer)

	outContent := []byte("remote-output-bytes")
	require.NoError(t, provider.Storage.SaveLocalFile("remote-out.txt", bytes.NewReader(outContent)))
	meta, ok := provider.Storage.GetFileMeta("remote-out.txt")
	require.True(t, ok)

	require.NoError(t, provider.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "make-blob",
		Parameters: map[string]protocol.ServiceParameter{
			"label": {Type: protocol.ParamTypeString, Required: false},
		},
	}, compute.BuildUnaryHandler(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{
			protocol.OutputHashKey: meta.Hash,
			protocol.OutputNameKey: "remote-out.txt",
			protocol.OutputSizeKey: float64(meta.Size),
			"file":                 protocol.VFSURI(meta.Hash),
		}, nil
	})))

	resp, err := consumer.LocalServiceRun("make-blob", `{"label":"x"}`)
	require.NoError(t, err)
	require.Equal(t, "completed", resp.Status)

	localPath := protocol.ResultLocalPath(resp.Outputs)
	require.NotEmpty(t, localPath)
	got, err := os.ReadFile(localPath)
	require.NoError(t, err)
	require.Equal(t, outContent, got)
}

func TestDispatchTaskRejectsMissingLocalPath(t *testing.T) {
	t.Parallel()

	provider := NewServer(t, testutil.DefaultConfig(t, "miss-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "miss-consumer"), nil)
	linkClusterPeers(t, provider, consumer)

	require.NoError(t, provider.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "echo-path",
	}, compute.BuildUnaryHandler(func(_ context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	missing := filepath.Join(t.TempDir(), "does-not-exist.bin")
	err := consumer.DispatchTask(provider.Config.ID, protocol.TaskRequest{
		TaskID:  "miss-task",
		Service: "echo-path",
		Payload: map[string]any{"file": missing},
		ReplyTo: consumer.Config.Address + protocol.PathServicesCallback,
	})
	require.Error(t, err, "missing local paths that look like filesystem paths must fail staging")
	require.Contains(t, err.Error(), "does-not-exist.bin")
}

func TestWebRTCSignalingRoundTripOverMTLS(t *testing.T) {
	t.Parallel()

	answerer := NewServer(t, testutil.DefaultConfig(t, "webrtc-answerer"), nil)
	offerer := NewServer(t, testutil.DefaultConfig(t, "webrtc-offerer"), nil)
	linkClusterPeers(t, answerer, offerer)

	signalURL := answerer.Config.Address + protocol.PathWebRTCSignal
	handler := compute.BuildWebRTCHandlerWithClient(signalURL, 5*time.Second, offerer.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 4)
	errCh := make(chan error, 1)
	go func() {
		errCh <- handler.ExecuteStream(ctx, in, out)
	}()

	ping := map[string]any{"ping": true, "n": float64(1)}
	in <- ping
	close(in)

	select {
	case chunk, ok := <-out:
		require.True(t, ok, "expected echoed DataChannel chunk")
		require.Equal(t, ping, chunk)
	case err := <-errCh:
		require.NoError(t, err)
		t.Fatal("handler finished without echo")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for mTLS WebRTC echo")
	}

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("WebRTC mTLS handler did not terminate cleanly")
	}
}
