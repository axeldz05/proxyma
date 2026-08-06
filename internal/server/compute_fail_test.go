package server_test

import (
	"context"
	"errors"
	"fmt"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequestServiceToClusterNoBids(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "lonely"), nil)

	_, _, _, err := sv.RequestServiceToCluster(protocol.DiscoveryQuery{Service: "missing-svc"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no nodes available")
}

func TestRequestServiceToClusterIgnoresFailedPeerBids(t *testing.T) {
	t.Parallel()
	mock := &testutil.MockPeerClient{
		OnFetchServiceBid: func(ctx context.Context, addr string, q protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			return protocol.ServiceBid{}, errors.New("peer unreachable")
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "bidder"), mock)
	sv.AddPeer("dead-peer", protocol.AddressRecord{Addresses: []string{"https://dead.invalid"}})

	_, _, _, err := sv.RequestServiceToCluster(protocol.DiscoveryQuery{Service: "ocr"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no nodes available")
}

func TestDispatchTaskMarksFailedWhenSubmitErrors(t *testing.T) {
	t.Parallel()
	mock := &testutil.MockPeerClient{
		OnSubmitTask: func(ctx context.Context, addr string, req protocol.TaskRequest) error {
			return fmt.Errorf("submit refused")
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "submit-fail"), mock)
	sv.AddPeer("peer-b", protocol.AddressRecord{Addresses: []string{"https://peer-b.invalid"}})
	sv.SetPeerOnline("peer-b", true)

	taskID := "fail-submit-1"
	err := sv.DispatchTask("peer-b", protocol.TaskRequest{
		TaskID:  taskID,
		Service: "anything",
		Payload: map[string]any{},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "submit refused")

	require.Eventually(t, func() bool {
		r, ok := sv.Compute.GetTaskResponse(taskID)
		return ok && r.Status == "failed"
	}, 2*time.Second, 50*time.Millisecond)

	r, ok := sv.Compute.GetTaskResponse(taskID)
	require.True(t, ok)
	require.Equal(t, "failed", r.Status)
	errMsg, _ := r.Outputs["error"].(string)
	require.Contains(t, errMsg, "submit refused")
}

func TestLocalServiceRunFailsWhenServiceUnknown(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "no-svc"), nil)
	_, err := sv.LocalServiceRun("does-not-exist", `{}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to discover service")
}

func TestServiceBidPrefersPeerWhenLocalCostHigher(t *testing.T) {
	t.Parallel()

	mock := &testutil.MockPeerClient{
		OnFetchServiceBid: func(ctx context.Context, peerID string, q protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			return protocol.ServiceBid{
				NodeID:          "fast-peer",
				NodeAddr:        "https://fast-peer.invalid",
				Schema:          protocol.ServiceSchema{Name: "ocr"},
				EstimatedMillis: 50,
				CanAccept:       true,
			}, nil
		},
	}
	sv := NewServer(t, testutil.DefaultConfig(t, "bidder-local"), mock)
	sv.AddPeer("fast-peer", protocol.AddressRecord{Addresses: []string{"https://fast-peer.invalid"}})
	sv.SetPeerOnline("fast-peer", true)

	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "ocr",
		Parameters: map[string]protocol.ServiceParameter{
			"file": {Type: protocol.ParamTypeFile, Required: true},
		},
	}, compute.BuildUnaryHandler(func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	localEst, ok := sv.Compute.EstimateTaskCost(protocol.DiscoveryQuery{Service: "ocr"})
	require.True(t, ok)
	require.Greater(t, localEst, int64(50), "local estimate must exceed peer bid so peer wins")

	peerID, _, _, err := sv.RequestServiceToCluster(protocol.DiscoveryQuery{
		Service:      "ocr",
		SortStrategy: protocol.StrategyFastest,
	})
	require.NoError(t, err)
	require.Equal(t, "fast-peer", peerID, "must not sticky-select local via hardcoded 10ms")

	for _, strategy := range []string{"", protocol.StrategyCheapest, protocol.StrategyLowPower} {
		peerID, _, _, err = sv.RequestServiceToCluster(protocol.DiscoveryQuery{
			Service:      "ocr",
			SortStrategy: strategy,
		})
		require.NoError(t, err)
		require.Equal(t, "fast-peer", peerID, "strategy %q", strategy)
	}
}
