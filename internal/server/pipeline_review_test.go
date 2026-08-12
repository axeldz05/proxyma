package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func TestWaitTaskResponseStopsOnContextCancellation(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := srv.waitTaskResponse(ctx, "never", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitTaskResponse error = %v, want context.Canceled", err)
	}
}

func TestPipelineNotificationDeliveryIsOwnedByServerLifecycle(t *testing.T) {
	t.Parallel()

	notifyStarted := make(chan struct{})
	releaseNotify := make(chan struct{})
	var startedOnce sync.Once
	client := &testutil.MockPeerClient{
		OnNotifyPipelineSchema: func(context.Context, string, protocol.PipelineNotification) error {
			startedOnce.Do(func() { close(notifyStarted) })
			<-releaseNotify
			return nil
		},
	}
	srv := newLifecycleServer(t, client)
	_, _ = srv.Peers.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b.invalid"}})
	srv.Peers.SetPeerOnline("peer-b", true)

	schema := protocol.PipelineSchema{
		ID:      "owned-notification",
		Version: 1,
		Steps:   []protocol.PipelineStep{{ID: "step", Service: "service"}},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal pipeline: %v", err)
	}
	if err := srv.LocalPipelineAdd(string(encoded)); err != nil {
		t.Fatalf("add pipeline: %v", err)
	}
	waitServerLifecycleSignal(t, notifyStarted, "pipeline notification start")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()
	waitServerLifecycleSignal(t, srv.lifetimeCtx.Done(), "server lifetime cancellation")
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before pipeline notification exited: %v", err)
	default:
	}

	close(releaseNotify)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("shutdown did not join pipeline notification")
	}
}

func TestDispatchRejectsContinuationWithoutPipelineStateCapability(t *testing.T) {
	t.Parallel()

	var submissions atomic.Int32
	client := &testutil.MockPeerClient{
		OnFetchServiceBid: func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			return protocol.ServiceBid{CanAccept: true}, nil
		},
		OnSubmitTask: func(context.Context, string, protocol.TaskRequest) error {
			submissions.Add(1)
			return nil
		},
	}
	srv := newLifecycleServer(t, client)
	schema := protocol.PipelineSchema{
		ID:      "capability-pipeline",
		Version: 1,
		Steps: []protocol.PipelineStep{
			{ID: "first", Service: "first"},
			{ID: "second", Service: "second"},
		},
	}
	if err := srv.Compute.RegisterPipeline(schema); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	request := protocol.TaskRequest{
		TaskID:  "continuation-capability",
		Service: schema.ID,
		Payload: map[string]any{},
	}
	if err := srv.Compute.BindPipelineTask(&request); err != nil {
		t.Fatalf("bind pipeline: %v", err)
	}
	request.PipelineState.CurrentStep = 1
	request.PipelineState.Outputs["first"] = map[string]any{"value": 1}
	request.PipelineState.OutputProducers["first"] = "first-worker"

	err := srv.DispatchTask("old-peer", request)
	if err == nil || !strings.Contains(err.Error(), "does not support pipeline state") {
		t.Fatalf("dispatch error = %v, want capability rejection", err)
	}
	if submissions.Load() != 0 {
		t.Fatal("continuation reached an incompatible peer")
	}
}

func TestDispatchKeepsInitialUnaryCompatibleWithLegacyPeer(t *testing.T) {
	t.Parallel()

	var bidChecks, submissions atomic.Int32
	client := &testutil.MockPeerClient{
		OnFetchServiceBid: func(context.Context, string, protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			bidChecks.Add(1)
			return protocol.ServiceBid{}, nil
		},
		OnSubmitTask: func(context.Context, string, protocol.TaskRequest) error {
			submissions.Add(1)
			return nil
		},
	}
	srv := newLifecycleServer(t, client)
	err := srv.DispatchTask("legacy-peer", protocol.TaskRequest{
		TaskID:  "legacy-unary",
		Service: "unary",
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("dispatch initial unary task: %v", err)
	}
	if bidChecks.Load() != 0 || submissions.Load() != 1 {
		t.Fatalf("bid checks=%d submissions=%d, want 0 and 1", bidChecks.Load(), submissions.Load())
	}
}

func TestServiceDiscoverySkipsLegacyPeerForContinuation(t *testing.T) {
	t.Parallel()

	client := &testutil.MockPeerClient{
		OnFetchServiceBid: func(_ context.Context, peerID string, _ protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			bid := protocol.ServiceBid{
				NodeID:          peerID,
				EstimatedMillis: 1,
				CanAccept:       true,
				Schema:          protocol.ServiceSchema{Name: "step"},
			}
			if peerID == "capable-peer" {
				bid.EstimatedMillis = 100
				bid.Capabilities = map[string]int{
					protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
				}
			}
			return bid, nil
		},
	}
	srv := newLifecycleServer(t, client)
	for _, peerID := range []string{"legacy-peer", "capable-peer"} {
		_, _ = srv.Peers.AddPeer(peerID, protocol.AddressRecord{
			Addresses: []string{"https://" + peerID + ".invalid"},
		})
		srv.Peers.SetPeerOnline(peerID, true)
	}

	selected, _, _, err := srv.RequestServiceToCluster(protocol.DiscoveryQuery{
		Service: "step",
		RequiredCapabilities: map[string]int{
			protocol.CapabilityPipelineState: protocol.PipelineStateCapabilityVersion,
		},
	})
	if err != nil {
		t.Fatalf("discover capable continuation peer: %v", err)
	}
	if selected != "capable-peer" {
		t.Fatalf("selected peer = %q, want capable-peer", selected)
	}
}
