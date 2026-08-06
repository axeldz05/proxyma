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
	require.Equal(t, http.StatusInternalServerError, respJoin.StatusCode)
}
