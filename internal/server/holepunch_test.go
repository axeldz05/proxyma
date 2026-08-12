package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHolePunch_HigherIDInitiatorEstablishesSession(t *testing.T) {
	// apple < zebra lexicographically → apple must dial when zebra initiates.
	appleCfg := testutil.DefaultConfig(t, "apple")
	zebraCfg := testutil.DefaultConfig(t, "zebra")
	isSponsorFalse := false
	appleCfg.IsSponsorOverride = &isSponsorFalse
	zebraCfg.IsSponsorOverride = &isSponsorFalse

	appleSrv := NewServer(t, appleCfg, nil)
	zebraSrv := NewServer(t, zebraCfg, nil)

	appleSrv.AddPeer("zebra", protocol.AddressRecord{Addresses: []string{zebraSrv.Config.Address}})
	zebraSrv.AddPeer("apple", protocol.AddressRecord{Addresses: []string{appleSrv.Config.Address}})

	appleUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = appleUDP.Close() })

	zebraUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = zebraUDP.Close() })

	appleQM := p2p.NewQUICManager(
		"apple", appleUDP,
		appleSrv.ClientTLSConfig(), appleSrv.ServerTLSConfig(),
		appleSrv.MountHandlers(), appleCfg.Logger,
	)
	appleQM.SetPublicUDPAddr(appleUDP.LocalAddr().String())
	require.NoError(t, appleQM.StartListener())
	t.Cleanup(appleQM.Close)
	appleSrv.AttachQUICManager(appleQM)

	zebraQM := p2p.NewQUICManager(
		"zebra", zebraUDP,
		zebraSrv.ClientTLSConfig(), zebraSrv.ServerTLSConfig(),
		zebraSrv.MountHandlers(), zebraCfg.Logger,
	)
	zebraQM.SetPublicUDPAddr(zebraUDP.LocalAddr().String())
	require.NoError(t, zebraQM.StartListener())
	t.Cleanup(zebraQM.Close)
	zebraSrv.AttachQUICManager(zebraQM)

	sendRelayReq := func(targetPeer, action string, payload []byte) ([]byte, error) {
		require.Equal(t, "apple", targetPeer)
		require.Equal(t, protocol.PathHolePunchInit, action)
		req, err := http.NewRequest(http.MethodPost, appleSrv.Config.Address+action, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := zebraSrv.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("holepunch init status %d: %s", resp.StatusCode, body)
		}
		var msg p2p.HolePunchMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return nil, err
		}
		return body, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sess, err := zebraQM.InitiateHolePunch(ctx, "apple", []string{p2p.FormatQUICAddr(appleQM.PublicUDPAddress())}, sendRelayReq)
	require.NoError(t, err, "higher-ID initiator must establish session (apple dials)")
	require.NotNil(t, sess)

	require.Eventually(t, func() bool {
		_, ok := appleQM.GetSession("zebra")
		return ok
	}, 3*time.Second, 50*time.Millisecond, "apple should also hold the QUIC session")
}
