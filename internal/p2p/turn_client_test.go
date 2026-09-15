package p2p_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

// startTestTURNServer runs an in-process pion TURN server on loopback (both
// families) and returns its v4 and v6 "host:port" addresses.
func startTestTURNServer(t *testing.T, secret, realm string) (string, string) {
	t.Helper()
	conn4, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	conn6, err := net.ListenPacket("udp6", "[::1]:0")
	require.NoError(t, err)
	srv, err := turn.NewServer(turn.ServerConfig{
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: conn4,
				RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
					RelayAddress: net.ParseIP("127.0.0.1"),
					Address:      "0.0.0.0",
					MinPort:      55000,
					MaxPort:      55100,
					MaxRetries:   100,
				},
			},
			{
				PacketConn: conn6,
				RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
					RelayAddress: net.ParseIP("::1"),
					Address:      "::",
					MinPort:      55000,
					MaxPort:      55100,
					MaxRetries:   100,
				},
			},
		},
		Realm:       realm,
		AuthHandler: turn.LongTermTURNRESTAuthHandler(secret, nil),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { _ = conn4.Close() })
	t.Cleanup(func() { _ = conn6.Close() })
	return conn4.LocalAddr().String(), conn6.LocalAddr().String()
}

func TestNewTURNManagerRequiresConfig(t *testing.T) {
	t.Parallel()

	_, err := p2p.NewTURNManager(p2p.TURNManagerConfig{ServerAddr: "", Secret: "s"})
	require.Error(t, err)
	_, err = p2p.NewTURNManager(p2p.TURNManagerConfig{ServerAddr: "127.0.0.1:3478", Secret: ""})
	require.Error(t, err)
}

func TestIPLiteralsFromAddresses(t *testing.T) {
	t.Parallel()

	ips := p2p.IPLiteralsFromAddresses([]string{
		"https://192.0.2.10:8443",
		p2p.FormatQUICAddr("192.0.2.10:45000"),
		"https://node-a.proxyma.local:8443",
		"198.51.100.9:8080",
	})
	got := make([]string, 0, len(ips))
	for _, ip := range ips {
		got = append(got, ip.String())
	}
	require.ElementsMatch(t, []string{"192.0.2.10", "198.51.100.9"}, got)
}

// TestTURNManagerRelayedQUICSession covers the full TURN fallback path: an
// allocation on an embedded TURN server, permissions for the peer, and a QUIC
// mTLS session established through the relay with a working HTTP round trip.
func TestTURNManagerRelayedQUICSession(t *testing.T) {
	t.Parallel()

	secret := "test-cluster-secret"
	turnAddr, _ := startTestTURNServer(t, secret, protocol.TURNDefaultRealm)

	caDir := t.TempDir()
	testutil.InitClusterCA(t, caDir)
	nodeA := testutil.IssueNode(t, caDir, t.TempDir(), "node-a")
	nodeB := testutil.IssueNode(t, caDir, t.TempDir(), "node-b")

	logger := protocol.NewLogger(testutil.TestLogWriter{T: t}, false)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("relayed!"))
	})

	udpB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	require.NoError(t, err)
	qmB := p2p.NewQUICManager("node-b", udpB, nodeB.ClientTLS, nodeB.ServerTLS, handler, logger)
	require.NoError(t, qmB.StartListener())
	t.Cleanup(qmB.Close)

	udpA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	require.NoError(t, err)
	qmA := p2p.NewQUICManager("node-a", udpA, nodeA.ClientTLS, nodeA.ServerTLS, handler, logger)
	t.Cleanup(qmA.Close)

	tm, err := p2p.NewTURNManager(p2p.TURNManagerConfig{
		ServerAddr: turnAddr,
		Secret:     secret,
		LocalID:    "node-a",
		Logger:     logger,
	})
	require.NoError(t, err)
	tm.SetQUICManager(qmA)
	t.Cleanup(tm.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	relayedAddr, err := tm.EnsureAllocation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, relayedAddr)
	require.Equal(t, relayedAddr, tm.RelayedAddress())

	sess, err := tm.DialPeer(ctx, "node-b", []string{p2p.FormatQUICAddr(udpB.LocalAddr().String())})
	require.NoError(t, err)

	// The callee accepted the inbound session through the relayed transport.
	require.Eventually(t, func() bool {
		_, exists := qmB.GetSession("node-a")
		return exists
	}, 5*time.Second, 20*time.Millisecond)
	_, exists := qmA.GetSession("node-b")
	require.True(t, exists)

	// HTTP round trip over the relayed QUIC session.
	stream, err := sess.OpenStreamSync(ctx)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://node-b.proxyma.local/manifest", nil)
	require.NoError(t, err)
	require.NoError(t, req.Write(stream))
	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "relayed!", string(body))

	require.NoError(t, sess.CloseWithError(0, "done"))
}

// TestTURNManagerIPv6Allocation checks family selection: with only IPv6
// desired peers and a dual-family TURN server, the allocation lands on the
// IPv6 relay.
func TestTURNManagerIPv6Allocation(t *testing.T) {
	t.Parallel()

	secret := "test-cluster-secret"
	_, turnV6Addr := startTestTURNServer(t, secret, protocol.TURNDefaultRealm)

	logger := protocol.NewLogger(testutil.TestLogWriter{T: t}, false)
	tm, err := p2p.NewTURNManager(p2p.TURNManagerConfig{
		ServerAddr: turnV6Addr,
		Secret:     secret,
		LocalID:    "node-a",
		Logger:     logger,
	})
	require.NoError(t, err)
	t.Cleanup(tm.Close)

	// Peer addresses learned before the allocation steer the family choice.
	tm.SetPeerAddresses([]string{p2p.FormatQUICAddr("[2001:db8::10]:45000")})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relayed, err := tm.EnsureAllocation(ctx)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(relayed)
	require.NoError(t, err)
	require.Equal(t, "::1", host)
}

// TestQUICManagerDialOverTURNWithoutAttach ensures the TURN dial path fails
// fast instead of panicking when no allocation was attached.
func TestQUICManagerDialOverTURNWithoutAttach(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	require.NoError(t, err)
	qm := p2p.NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	_, err = qm.DialOverTURN(context.Background(), "peer", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1})
	require.Error(t, err)
}
