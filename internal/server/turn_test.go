package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"proxyma/internal/utils"
)

func testTurnConfig(t *testing.T) protocol.NodeConfig {
	t.Helper()
	return protocol.NodeConfig{
		ID:     "turn-node",
		Logger: protocol.NewLogger(testutil.TestLogWriter{T: t}, true),
	}
}

func TestStartTURNServerRequiresSecret(t *testing.T) {
	t.Parallel()

	cfg := testTurnConfig(t)
	ts, err := startTURNServer(cfg, "127.0.0.1", "")
	require.Error(t, err)
	require.Nil(t, ts)
	require.Contains(t, err.Error(), "turn_secret")
}

func TestStartTURNServerRejectsInvalidRelayIP(t *testing.T) {
	t.Parallel()

	cfg := testTurnConfig(t)
	cfg.TURNSecret = "cluster-secret"
	ts, err := startTURNServer(cfg, "not-an-ip", "")
	require.Error(t, err)
	require.Nil(t, ts)
}

// TestStartTURNServerAuthenticatesAllocations drives a real pion TURN client
// against the embedded server: valid REST-style credentials must allocate a
// relayed address, expired ones must be rejected.
func TestStartTURNServerAuthenticatesAllocations(t *testing.T) {
	t.Parallel()

	cfg := testTurnConfig(t)
	cfg.TURNSecret = "cluster-secret"
	cfg.TURNListenAddr = "127.0.0.1:0"
	ts, err := startTURNServer(cfg, "127.0.0.1", "")
	require.NoError(t, err)
	t.Cleanup(ts.Close)
	require.Len(t, ts.conns, 1)

	allocateWith := func(username, password string) (string, error) {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		client, err := turn.NewClient(&turn.ClientConfig{
			TURNServerAddr: ts.conns[0].LocalAddr().String(),
			Conn:           conn,
			Username:       username,
			Password:       password,
			Realm:          protocol.TURNDefaultRealm,
			LoggerFactory:  utils.PionLoggerFactory(cfg.Logger),
		})
		require.NoError(t, err)
		require.NoError(t, client.Listen())
		t.Cleanup(client.Close)
		relayed, err := client.Allocate()
		if err != nil {
			return "", err
		}
		return relayed.LocalAddr().String(), nil
	}

	username, password, err := turn.GenerateLongTermTURNRESTCredentials(cfg.TURNSecret, "peer-a", time.Hour)
	require.NoError(t, err)

	relayed, allocErr := allocateWith(username, password)
	require.NoError(t, allocErr)
	require.NotEmpty(t, relayed)
	host, port, err := net.SplitHostPort(relayed)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	require.NotEqual(t, 0, port)

	// Expired credential window must be rejected by the server.
	expiredUser, expiredPass, err := turn.GenerateLongTermTURNRESTCredentials(cfg.TURNSecret, "peer-b", -time.Minute)
	require.NoError(t, err)
	_, allocErr = allocateWith(expiredUser, expiredPass)
	require.Error(t, allocErr)
}

// TestStartTURNServerServesBothFamilies starts the server with IPv4 and IPv6
// relay addresses and allocates on each family.
func TestStartTURNServerServesBothFamilies(t *testing.T) {
	t.Parallel()

	cfg := testTurnConfig(t)
	cfg.TURNSecret = "cluster-secret"
	cfg.TURNListenAddr = "127.0.0.1:0"
	ts, err := startTURNServer(cfg, "127.0.0.1", "::1")
	require.NoError(t, err)
	t.Cleanup(ts.Close)
	require.Len(t, ts.conns, 2)

	allocateWith := func(network, serverAddr string, family turn.RequestedAddressFamily) string {
		bindIP := net.IPv4zero
		if network == "udp6" {
			bindIP = net.IPv6unspecified
		}
		conn, err := net.ListenUDP(network, &net.UDPAddr{IP: bindIP, Port: 0})
		if err != nil {
			t.Fatalf("bind %s: %v", network, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		username, password, err := turn.GenerateLongTermTURNRESTCredentials(cfg.TURNSecret, "peer", time.Hour)
		require.NoError(t, err)
		client, err := turn.NewClient(&turn.ClientConfig{
			TURNServerAddr:         serverAddr,
			Conn:                   conn,
			Username:               username,
			Password:               password,
			Realm:                  protocol.TURNDefaultRealm,
			RequestedAddressFamily: family,
			LoggerFactory:          utils.PionLoggerFactory(cfg.Logger),
		})
		require.NoError(t, err)
		require.NoError(t, client.Listen())
		t.Cleanup(client.Close)
		relayed, err := client.Allocate()
		require.NoError(t, err)
		t.Cleanup(func() { _ = relayed.Close() })
		return relayed.LocalAddr().String()
	}

	var v6ConnAddr string
	for _, conn := range ts.conns {
		host, _, err := net.SplitHostPort(conn.LocalAddr().String())
		if err == nil && net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
			v6ConnAddr = conn.LocalAddr().String()
		}
	}
	require.NotEmpty(t, v6ConnAddr)

	v4Relayed := allocateWith("udp4", ts.conns[0].LocalAddr().String(), turn.RequestedAddressFamilyIPv4)
	host4, _, err := net.SplitHostPort(v4Relayed)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host4)

	v6Relayed := allocateWith("udp6", v6ConnAddr, turn.RequestedAddressFamilyIPv6)
	host6, _, err := net.SplitHostPort(v6Relayed)
	require.NoError(t, err)
	require.Equal(t, "::1", host6)
}

// TestTurnRelayAddressesPicksPerFamily covers the per-family resolution order:
// comma-separated turn_public_addr first, then the discovered public IP, then
// local routable IPs.
func TestTurnRelayAddressesPicksPerFamily(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.Config.TURNPublicAddr = "203.0.113.7, 2001:db8::7"
	v4, v6 := srv.turnRelayAddresses("198.51.100.1")
	require.Equal(t, "203.0.113.7", v4)
	require.Equal(t, "2001:db8::7", v6)

	v4, v6 = srv.turnRelayAddresses("198.51.100.1")
	require.Equal(t, "203.0.113.7", v4)
	require.Equal(t, "2001:db8::7", v6)

	// Without config or discovered IP the local interfaces decide; whatever
	// comes back must be a valid IP of the requested family (possibly empty).
	v4, v6 = srv.turnRelayAddresses("")
	if v4 != "" {
		require.NotNil(t, net.ParseIP(v4).To4())
	}
	if v6 != "" {
		require.Nil(t, net.ParseIP(v6).To4())
	}
}

// TestBuildPresenceAddressesAnnouncesTURNUDPAddr ensures the relayed endpoint
// rides the presence announcement as a quic:// address so peers can dial it.
func TestBuildPresenceAddressesAnnouncesTURNUDPAddr(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.Config.Address = protocol.HTTPSAddr("node-id", "8443")
	turnAddr := net.JoinHostPort("203.0.113.50", "55000")
	got := srv.buildPresenceAddresses(
		"8443",
		NATState{PublicUDPAddr: "203.0.113.50:45000", TURNUDPAddr: turnAddr},
		nil,
	)
	require.Contains(t, got, p2p.FormatQUICAddr(turnAddr))
}

// TestLocalQUICWithTURNCallee covers the STUN-blocked node: without any public
// endpoint the QUIC manager must still exist (so TURN dialing works) and the
// TURN allocation must be attached and announced.
func TestLocalQUICWithTURNCallee(t *testing.T) {
	t.Parallel()

	// Embedded TURN server the node will allocate against.
	turnCfg := testTurnConfig(t)
	turnCfg.TURNSecret = "cluster-secret"
	turnCfg.TURNListenAddr = "127.0.0.1:0"
	ts, err := startTURNServer(turnCfg, "127.0.0.1", "")
	require.NoError(t, err)
	t.Cleanup(ts.Close)

	// turn_server/turn_secret must be present before New so the TURN manager
	// is created with the node.
	cfg := protocol.NodeConfig{
		ID:          "stun-blocked-node",
		StoragePath: t.TempDir(),
		Workers:     1,
		Logger:      protocol.NewLogger(testutil.TestLogWriter{T: t}, true),
		STUNServer:  "999.999.999.999:19302", // fails fast, twice
		DisableUPnP: true,
		TURNServer:  ts.conns[0].LocalAddr().String(),
		TURNSecret:  "cluster-secret",
	}
	srv, err := New(cfg, &testutil.MockPeerClient{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	node := testutil.NewNodeTLS(t, srv.Config.ID)
	srv.SetTLSConfigs(node.ServerTLS, node.ClientTLS)

	// The failed NAT check must still produce a local QUIC manager plus an
	// attached, announced TURN allocation.
	srv.CheckNAT()
	require.Eventually(t, func() bool {
		state := srv.CurrentNATState()
		return state.QUICManager != nil && state.TURNUDPAddr != ""
	}, 10*time.Second, 50*time.Millisecond)
	state := srv.CurrentNATState()
	require.Empty(t, state.PublicUDPAddr)

	// Presence must announce the relayed endpoint even without a direct one.
	addrs := srv.buildPresenceAddresses("8443", state, nil)
	require.Contains(t, addrs, p2p.FormatQUICAddr(state.TURNUDPAddr))
}
