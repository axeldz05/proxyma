package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestServiceStrategiesChooseProviderThroughPublicResult(t *testing.T) {
	t.Parallel()

	var requester, cheapProvider, lowPowerProvider *TestServer
	mock := &testutil.MockPeerClient{
		OnFetchServiceBid: func(_ context.Context, peerID string, _ protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
			switch peerID {
			case "strategy-cheap":
				return protocol.ServiceBid{
					NodeID:          peerID,
					EstimatedMillis: 200,
					CostUnits:       10,
					PowerScore:      900,
					CanAccept:       true,
					Schema:          protocol.ServiceSchema{Name: "strategy/identify"},
				}, nil
			case "strategy-low-power":
				return protocol.ServiceBid{
					NodeID:          peerID,
					EstimatedMillis: 100,
					CostUnits:       500,
					PowerScore:      10,
					CanAccept:       true,
					Schema:          protocol.ServiceSchema{Name: "strategy/identify"},
				}, nil
			default:
				return protocol.ServiceBid{}, fmt.Errorf("unexpected bid peer %q", peerID)
			}
		},
		OnSubmitTask: func(_ context.Context, peerID string, task protocol.TaskRequest) error {
			var target *TestServer
			switch peerID {
			case "strategy-cheap":
				target = cheapProvider
			case "strategy-low-power":
				target = lowPowerProvider
			default:
				return fmt.Errorf("unexpected submit peer %q", peerID)
			}
			body, err := json.Marshal(task)
			if err != nil {
				return err
			}
			req, err := http.NewRequest(
				http.MethodPost,
				target.Config.Address+protocol.PathServicesSubmit,
				bytes.NewReader(body),
			)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := requester.Client().Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusAccepted {
				return fmt.Errorf("submit status %d", resp.StatusCode)
			}
			return nil
		},
	}

	requester = NewServer(t, testutil.DefaultConfig(t, "strategy-requester"), mock)
	cheapProvider = NewServer(t, testutil.DefaultConfig(t, "strategy-cheap"), nil)
	lowPowerProvider = NewServer(t, testutil.DefaultConfig(t, "strategy-low-power"), nil)
	linkClusterPeers(t, requester, cheapProvider, lowPowerProvider)

	register := func(node *TestServer) {
		nodeID := node.Config.ID
		require.NoError(t, node.Compute.RegisterNewService(
			protocol.ServiceSchema{
				Name: "strategy/identify",
				Outputs: map[string]protocol.ServiceParameter{
					"provider": {Type: protocol.ParamTypeString},
				},
			},
			compute.BuildUnaryHandler(func(context.Context, map[string]any) (map[string]any, error) {
				return map[string]any{"provider": nodeID}, nil
			}),
		))
	}
	register(cheapProvider)
	register(lowPowerProvider)

	cheapest, err := requester.LocalServiceRun("strategy/identify", `{}`, protocol.StrategyCheapest)
	require.NoError(t, err)
	require.Equal(t, "completed", cheapest.Status)
	require.Equal(t, cheapProvider.Config.ID, cheapest.Outputs["provider"])

	lowPower, err := requester.LocalServiceRun("strategy/identify", `{}`, protocol.StrategyLowPower)
	require.NoError(t, err)
	require.Equal(t, "completed", lowPower.Status)
	require.Equal(t, lowPowerProvider.Config.ID, lowPower.Outputs["provider"])
}
