package server_test

import (
	"context"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectBestServiceBidHonorsCheapestAndLowPower(t *testing.T) {
	t.Parallel()

	bids := []protocol.ServiceBid{
		{NodeID: "A", EstimatedMillis: 200, CostUnits: 50, PowerScore: 900, CanAccept: true},  // cheap, high power
		{NodeID: "B", EstimatedMillis: 80, CostUnits: 500, PowerScore: 100, CanAccept: true},  // fast, low power, expensive
		{NodeID: "C", EstimatedMillis: 120, CostUnits: 200, PowerScore: 400, CanAccept: true}, // middle
	}

	fastest := server.SelectBestServiceBid(bids, protocol.StrategyFastest)
	require.Equal(t, "B", fastest.NodeID)

	cheapest := server.SelectBestServiceBid(bids, protocol.StrategyCheapest)
	require.Equal(t, "A", cheapest.NodeID)

	lowPower := server.SelectBestServiceBid(bids, protocol.StrategyLowPower)
	require.Equal(t, "B", lowPower.NodeID)

	def := server.SelectBestServiceBid(bids, "")
	require.Equal(t, "B", def.NodeID, "empty strategy defaults to fastest")

	// Deterministic tie-break by NodeID
	tied := []protocol.ServiceBid{
		{NodeID: "z", EstimatedMillis: 10, CanAccept: true},
		{NodeID: "a", EstimatedMillis: 10, CanAccept: true},
	}
	require.Equal(t, "a", server.SelectBestServiceBid(tied, protocol.StrategyFastest).NodeID)
}

func TestServiceBidIncludesLiveResourceScore(t *testing.T) {
	t.Parallel()

	restore := compute.SetHostResourceSampler(func() (cpuLoad, memPressure float64) {
		return 0.75, 0.4
	})
	t.Cleanup(restore)

	sv := NewServer(t, testutil.DefaultConfig(t, "bid-resources"), nil)
	require.NoError(t, sv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name: "ocr",
		Parameters: map[string]protocol.ServiceParameter{
			"file": {Type: protocol.ParamTypeFile, Required: true},
		},
	}, compute.BuildUnaryHandler(func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})))

	bid, ok := sv.Compute.BuildServiceBid(protocol.DiscoveryQuery{Service: "ocr"})
	require.True(t, ok)
	require.True(t, bid.CanAccept)
	require.Equal(t, 0.75, bid.CPULoad)
	require.Equal(t, 0.4, bid.MemPressure)
	require.Greater(t, bid.CostUnits, bid.EstimatedMillis)
	require.Greater(t, bid.PowerScore, int64(0))

	restore2 := compute.SetHostResourceSampler(func() (cpuLoad, memPressure float64) {
		return 0.1, 0.1
	})
	t.Cleanup(restore2)

	bidLow, ok := sv.Compute.BuildServiceBid(protocol.DiscoveryQuery{Service: "ocr"})
	require.True(t, ok)
	require.Less(t, bidLow.CostUnits, bid.CostUnits)
	require.Less(t, bidLow.PowerScore, bid.PowerScore)
}
