package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestConcurrentJoinBurnsInviteOnce(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "sponsor-toctou"), nil)

	token, _, err := sv.LocalInviteGenerate(15)
	require.NoError(t, err)
	_, secret, err := p2p.ParseSmartToken(token)
	require.NoError(t, err)

	csrA, _, err := p2p.GenerateNodeCSR("joiner-a")
	require.NoError(t, err)
	csrB, _, err := p2p.GenerateNodeCSR("joiner-b")
	require.NoError(t, err)

	validAddress := "https://127.0.0.1:9"
	naked := testutil.InsecureHTTPClient()

	var okCount atomic.Int32
	var wg sync.WaitGroup
	join := func(id string, csr []byte) {
		defer wg.Done()
		body, _ := json.Marshal(protocol.JoinRequest{
			Secret:  secret,
			CSR:     string(csr),
			ID:      id,
			Address: validAddress,
		})
		resp, err := naked.Post(sv.Config.Address+protocol.PathClusterJoin, "application/json", bytes.NewBuffer(body))
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			okCount.Add(1)
		}
	}

	wg.Add(2)
	go join("joiner-a", csrA)
	go join("joiner-b", csrB)
	wg.Wait()

	require.Equal(t, int32(1), okCount.Load(), "single-use invite must sign exactly one concurrent join")
}

func TestGossipCannotElevateIsSponsorForRotate(t *testing.T) {
	t.Parallel()
	victim := NewServer(t, testutil.DefaultConfig(t, "victim-rotate"), nil)
	attacker := NewServer(t, testutil.DefaultConfig(t, "attacker-rotate"), nil)
	gossip := NewServer(t, testutil.DefaultConfig(t, "gossip-rotate"), nil)

	victim.AddPeer(attacker.Config.ID, protocol.AddressRecord{
		Addresses: []string{attacker.Config.Address},
		IsSponsor: false,
	})
	victim.AddPeer(gossip.Config.ID, protocol.AddressRecord{
		Addresses: []string{gossip.Config.Address},
	})
	attacker.AddPeer(victim.Config.ID, protocol.AddressRecord{
		Addresses: []string{victim.Config.Address},
	})
	gossip.AddPeer(victim.Config.ID, protocol.AddressRecord{
		Addresses: []string{victim.Config.Address},
	})

	payload := protocol.AddPeerRequest{
		ID: attacker.Config.ID,
		Address: protocol.AddressRecord{
			Addresses: []string{attacker.Config.Address},
			IsSponsor: true,
			Sequence:  99,
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, victim.Config.Address+protocol.PathPeersAdd, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := gossip.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rec, ok := victim.Peers.GetPeerRecord(attacker.Config.ID)
	require.True(t, ok)
	require.False(t, rec.IsSponsor, "gossip must not elevate IsSponsor")

	rotateBody, _ := json.Marshal(protocol.RotateTLSPayload{CACert: "fake", NodeCert: "fake"})
	rotReq, err := http.NewRequest(http.MethodPost, victim.Config.Address+protocol.PathClusterRotate, bytes.NewReader(rotateBody))
	require.NoError(t, err)
	rotReq.Header.Set("Content-Type", "application/json")
	rotResp, err := attacker.Client().Do(rotReq)
	require.NoError(t, err)
	defer func() { _ = rotResp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, rotResp.StatusCode)
}

func TestNonCACannotMintInvite(t *testing.T) {
	t.Parallel()
	peerCfg := testutil.DefaultConfig(t, "peer-no-ca")
	isSponsorFalse := false
	peerCfg.IsSponsorOverride = &isSponsorFalse
	peer := NewServer(t, peerCfg, nil)

	require.NoError(t, os.Remove(p2p.CAKeyPath(peer.Config.CAPath)))

	_, _, err := peer.LocalInviteGenerate(15)
	require.Error(t, err)

	body, _ := json.Marshal(protocol.InviteRequest{ValidForMinutes: 15})
	req, err := http.NewRequest(http.MethodPost, peer.Config.Address+protocol.PathPeersInvite, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := peer.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
