package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayLongPollingIntegration(t *testing.T) {
	t.Parallel()

	// 1. Start Server
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-relay"), nil)

	// Register target-node as a known peer of the sponsor
	sponsor.AddPeer("target-node", protocol.AddressRecord{
		Addresses: []string{"http://target-node.proxyma.local"},
	})

	// Issue and load certificate for target-node to simulate mTLS authentication
	caPath := filepath.Dir(sponsor.Config.StoragePath)
	err := p2p.IssueNodeCertificate(caPath, sponsor.Config.StoragePath, "target-node")
	require.NoError(t, err)

	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(sponsor.Config.StoragePath, "target-node.crt")
	nodeKeyFile := filepath.Join(sponsor.Config.StoragePath, "target-node.key")
	_, targetClientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)

	targetClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   targetClientTLS,
			DisableKeepAlives: true,
		},
	}

	// 2. Start a long poll from "target-node" using its specific mTLS client
	pollReq, _ := http.NewRequest(http.MethodGet, sponsor.Config.Address+"/relay/poll?id=target-node", nil)
	pollCtx, pollCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pollCancel()
	pollReq = pollReq.WithContext(pollCtx)

	pollResultCh := make(chan *http.Response)
	go func() {
		resp, _ := targetClient.Do(pollReq)
		pollResultCh <- resp
	}()

	time.Sleep(100 * time.Millisecond) // Let the poll arrive

	// 3. Send a forward request from "sender-node" destined to "target-node"
	relayReq := protocol.RelayRequest{
		ReqID:   "req-123",
		Target:  "target-node",
		Method:  "GET",
		Path:    "/some/test/path",
		Headers: map[string]string{"X-Test": "Hello"},
		Body:    []byte("hello relay"),
	}
	bodyBytes, _ := json.Marshal(relayReq)
	
	fwdReq, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/relay/forward", bytes.NewBuffer(bodyBytes))
	fwdReq.Header.Set("Content-Type", "application/json")
	
	fwdResultCh := make(chan *http.Response)
	go func() {
		resp, _ := sponsor.Client().Do(fwdReq)
		fwdResultCh <- resp
	}()

	// 4. target-node's poll should complete and receive the request
	pollResp := <-pollResultCh
	require.NotNil(t, pollResp)
	require.Equal(t, http.StatusOK, pollResp.StatusCode)

	var receivedReq protocol.RelayRequest
	err = json.NewDecoder(pollResp.Body).Decode(&receivedReq)
	require.NoError(t, err)
	require.Equal(t, "req-123", receivedReq.ReqID)
	require.Equal(t, "/some/test/path", receivedReq.Path)
	_ = pollResp.Body.Close()

	// 5. target-node sends the reply back
	relayRes := protocol.RelayResponse{
		ReqID:      "req-123",
		StatusCode: 201,
		Headers:    map[string]string{"X-Reply": "World"},
		Body:       []byte("response relay"),
	}
	resBytes, _ := json.Marshal(relayRes)
	
	replyReq, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/relay/reply", bytes.NewBuffer(resBytes))
	replyReq.Header.Set("Content-Type", "application/json")
	replyResp, err := targetClient.Do(replyReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replyResp.StatusCode)
	_ = replyResp.Body.Close()

	// 6. sender-node's forward request should complete with the response
	fwdResp := <-fwdResultCh
	require.NotNil(t, fwdResp)
	require.Equal(t, http.StatusOK, fwdResp.StatusCode)

	var finalRes protocol.RelayResponse
	err = json.NewDecoder(fwdResp.Body).Decode(&finalRes)
	require.NoError(t, err)
	require.Equal(t, 201, finalRes.StatusCode)
	require.Equal(t, "response relay", string(finalRes.Body))
	_ = fwdResp.Body.Close()
}
