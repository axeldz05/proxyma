package server_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinWithValidCSRSucceeds(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "csr-sponsor"), nil)

	reqBody := protocol.InviteRequest{ValidForMinutes: 15}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", sv.Config.Address+protocol.PathPeersInvite, bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var inviteResp protocol.InviteResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inviteResp))
	_, secret, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)

	csrPEM, _, err := p2p.GenerateNodeCSR("joiner-node")
	require.NoError(t, err)

	nakedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	joinReq := protocol.JoinRequest{
		Secret:  secret,
		CSR:     string(csrPEM),
		ID:      "joiner-node",
		Address: "https://127.0.0.1:19999",
	}
	joinBody, _ := json.Marshal(joinReq)
	respJoin, err := nakedClient.Post(sv.Config.Address+protocol.PathClusterJoin, "application/json", bytes.NewBuffer(joinBody))
	require.NoError(t, err)
	defer func() { _ = respJoin.Body.Close() }()
	require.Equal(t, http.StatusOK, respJoin.StatusCode)

	var joinResp protocol.JoinResponse
	require.NoError(t, json.NewDecoder(respJoin.Body).Decode(&joinResp))
	require.Contains(t, joinResp.Certificate, "CERTIFICATE")
	require.Contains(t, joinResp.CACert, "CERTIFICATE")
}

func TestJoinRejectsBadCSRAfterValidInvite(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "badcsr-sponsor"), nil)

	reqBody := protocol.InviteRequest{ValidForMinutes: 15}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", sv.Config.Address+protocol.PathPeersInvite, bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var inviteResp protocol.InviteResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inviteResp))
	_, secret, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)

	nakedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	joinReq := protocol.JoinRequest{
		Secret:  secret,
		CSR:     "not-a-real-csr",
		ID:      "bad",
		Address: "https://127.0.0.1:19998",
	}
	joinBody, _ := json.Marshal(joinReq)
	respJoin, err := nakedClient.Post(sv.Config.Address+protocol.PathClusterJoin, "application/json", bytes.NewBuffer(joinBody))
	require.NoError(t, err)
	defer func() { _ = respJoin.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, respJoin.StatusCode)
}

func TestClusterJoinRejectsCSRCommonNameMismatch(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "csr-cn-sponsor"), nil)

	reqBody := protocol.InviteRequest{ValidForMinutes: 15}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", sv.Config.Address+protocol.PathPeersInvite, bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var inviteResp protocol.InviteResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inviteResp))
	_, secret, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)

	csrPEM, _, err := p2p.GenerateNodeCSR("imposter")
	require.NoError(t, err)

	nakedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	joinReq := protocol.JoinRequest{
		Secret:  secret,
		CSR:     string(csrPEM),
		ID:      "honest-node",
		Address: "https://127.0.0.1:19997",
	}
	joinBody, _ := json.Marshal(joinReq)
	respJoin, err := nakedClient.Post(sv.Config.Address+protocol.PathClusterJoin, "application/json", bytes.NewBuffer(joinBody))
	require.NoError(t, err)
	defer func() { _ = respJoin.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, respJoin.StatusCode)

	// Matching CN+ID still works (fresh invite).
	req2, err := http.NewRequest("POST", sv.Config.Address+protocol.PathPeersInvite, bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp2, err := sv.Client().Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	var invite2 protocol.InviteResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&invite2))
	_, secret2, err := p2p.ParseSmartToken(invite2.Token)
	require.NoError(t, err)

	csrOK, _, err := p2p.GenerateNodeCSR("honest-node")
	require.NoError(t, err)
	joinOK := protocol.JoinRequest{
		Secret:  secret2,
		CSR:     string(csrOK),
		ID:      "honest-node",
		Address: "https://127.0.0.1:19996",
	}
	bodyOK, _ := json.Marshal(joinOK)
	respOK, err := nakedClient.Post(sv.Config.Address+protocol.PathClusterJoin, "application/json", bytes.NewBuffer(bodyOK))
	require.NoError(t, err)
	defer func() { _ = respOK.Body.Close() }()
	require.Equal(t, http.StatusOK, respOK.StatusCode)
	var joinResp protocol.JoinResponse
	require.NoError(t, json.NewDecoder(respOK.Body).Decode(&joinResp))
	require.Contains(t, joinResp.Certificate, "CERTIFICATE")
}
