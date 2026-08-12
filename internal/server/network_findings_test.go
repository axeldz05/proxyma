package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fd/go-nat"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

type readyNAT struct {
	externalIP net.IP
}

func (n *readyNAT) Type() string                        { return "test" }
func (n *readyNAT) GetDeviceAddress() (net.IP, error)   { return net.IPv4(192, 0, 2, 1), nil }
func (n *readyNAT) GetExternalAddress() (net.IP, error) { return n.externalIP, nil }
func (n *readyNAT) GetInternalAddress() (net.IP, error) { return net.IPv4(192, 0, 2, 2), nil }
func (n *readyNAT) DeletePortMapping(string, int) error { return nil }
func (n *readyNAT) AddPortMapping(_ string, port int, _ string, _ time.Duration) (int, error) {
	return port + 100, nil
}

func newNetworkFindingServer(t *testing.T, client *testutil.MockPeerClient) *Server {
	t.Helper()

	srv, err := New(protocol.NodeConfig{
		ID:          "network-finding-test",
		StoragePath: t.TempDir(),
		Workers:     1,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, client)
	if err != nil {
		t.Fatalf("create network finding server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown network finding server: %v", err)
		}
	})
	return srv
}

func TestNATSetupRetriesOneTransientSTUNFailureWithoutDuplicates(t *testing.T) {
	t.Parallel()

	stun, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen mock STUN: %v", err)
	}
	t.Cleanup(func() { _ = stun.Close() })

	var requests atomic.Int32
	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		for range 2 {
			buf := make([]byte, 1024)
			n, remote, readErr := stun.ReadFromUDP(buf)
			if readErr != nil || n < 20 {
				return
			}
			attempt := requests.Add(1)
			if attempt == 1 {
				// Syntactically valid response header, but no mapped-address
				// attribute. This is a transient protocol failure, not a timeout.
				resp := make([]byte, 20)
				binary.BigEndian.PutUint16(resp[0:2], 0x0101)
				binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)
				copy(resp[8:20], buf[8:20])
				_, _ = stun.WriteToUDP(resp, remote)
				continue
			}

			resp := make([]byte, 32)
			binary.BigEndian.PutUint16(resp[0:2], 0x0101)
			binary.BigEndian.PutUint16(resp[2:4], 12)
			binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)
			copy(resp[8:20], buf[8:20])
			binary.BigEndian.PutUint16(resp[20:22], 0x0020)
			binary.BigEndian.PutUint16(resp[22:24], 8)
			resp[25] = 0x01
			binary.BigEndian.PutUint16(resp[26:28], uint16(remote.Port)^0x2112)
			ip := remote.IP.To4()
			binary.BigEndian.PutUint32(resp[28:32], binary.BigEndian.Uint32(ip)^0x2112A442)
			_, _ = stun.WriteToUDP(resp, remote)
		}
	}()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.Config.STUNServer = stun.LocalAddr().String()
	srv.Config.DisableUPnP = true
	nodeTLS := testutil.NewNodeTLS(t, srv.Config.ID)
	srv.SetTLSConfigs(nodeTLS.ServerTLS, nodeTLS.ClientTLS)

	srv.CheckNAT()
	select {
	case <-responderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock STUN responder did not finish")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("STUN attempts = %d, want 2", got)
	}
	state := srv.CurrentNATState()
	if state.QUICManager == nil {
		t.Fatal("transient first STUN failure permanently prevented QUIC setup")
	}
	if srv.natMapper != nil {
		t.Fatal("successful STUN retry unexpectedly created a NAT mapper")
	}

	srv.CheckNAT()
	after := srv.CurrentNATState()
	if after.QUICManager != state.QUICManager {
		t.Fatal("repeated NAT check replaced the live QUIC listener")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("repeated NAT check sent %d STUN requests, want 2 total", got)
	}
}

func TestBuildPresenceAddressesIncludesRoutableIPv6(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.Config.Address = protocol.HTTPSAddr("node-id", "8443")
	srv.tcpFamilies = tcpFamilyIPv4 | tcpFamilyIPv6
	publicUDP := net.JoinHostPort("2001:db8::20", "45000")
	got := srv.buildPresenceAddresses(
		"8443",
		NATState{IsSponsor: true, PublicUDPAddr: publicUDP},
		[]net.IP{
			net.ParseIP("192.0.2.10"),
			net.ParseIP("2001:db8::10"),
			net.ParseIP("fe80::1"),
			net.IPv6loopback,
		},
	)

	assertAddress := func(want string) {
		t.Helper()
		for _, addr := range got {
			if addr == want {
				return
			}
		}
		t.Fatalf("addresses %v do not contain %q", got, want)
	}
	assertAddress(protocol.HTTPSAddr("192.0.2.10", "8443"))
	assertAddress(protocol.HTTPSAddr("2001:db8::10", "8443"))
	assertAddress(protocol.HTTPSAddr("2001:db8::20", "8443"))
	assertAddress(p2p.FormatQUICAddr(publicUDP))

	for _, forbidden := range []string{
		protocol.HTTPSAddr("fe80::1", "8443"),
		protocol.HTTPSAddr("::1", "8443"),
	} {
		for _, addr := range got {
			if addr == forbidden {
				t.Fatalf("non-routable IPv6 address %q was announced", forbidden)
			}
		}
	}
}

func TestBuildPresenceAddressesSuppressesUnsupportedIPv6TCP(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.Config.Address = protocol.HTTPSAddr("node-id", "8443")
	srv.tcpFamilies = tcpFamilyIPv4
	got := srv.buildPresenceAddresses(
		"8443",
		NATState{IsSponsor: true, PublicUDPAddr: net.JoinHostPort("2001:db8::20", "45000")},
		[]net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("2001:db8::10")},
	)

	for _, forbidden := range []string{
		protocol.HTTPSAddr("2001:db8::10", "8443"),
		protocol.HTTPSAddr("2001:db8::20", "8443"),
	} {
		for _, addr := range got {
			if addr == forbidden {
				t.Fatalf("IPv4-only listener advertised unsupported IPv6 TCP endpoint %q", forbidden)
			}
		}
	}
}

func TestServerWaitsForInitialNATMappingResult(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.natMapperFactory = func(logger *slog.Logger, tcpPort, udpPort int) *p2p.NATMapper {
		mapper := p2p.NewNATMapper(logger, tcpPort, udpPort)
		mapper.SetGatewayDiscovery(func() (nat.NAT, error) {
			return &readyNAT{externalIP: net.ParseIP("198.51.100.44")}, nil
		})
		return mapper
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	endpoint, publicKnown := srv.mapPortsWithUPnP(ctx, publicEndpoint{Conn: conn}, false)
	if !publicKnown {
		t.Fatal("server decided NAT status before the mapper's initial result")
	}
	if got, want := endpoint.IP, "198.51.100.44"; got != want {
		t.Fatalf("external IP = %q, want %q", got, want)
	}
	if got, want := endpoint.Port, localPort+100; got != want {
		t.Fatalf("mapped UDP port = %d, want %d", got, want)
	}
}

func TestServerNeverAppliesIPv4NATMapperToIPv6Endpoint(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	var factoryCalls atomic.Int32
	srv.natMapperFactory = func(logger *slog.Logger, tcpPort, udpPort int) *p2p.NATMapper {
		factoryCalls.Add(1)
		return p2p.NewNATMapper(logger, tcpPort, udpPort)
	}
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6unspecified})
	if err != nil {
		t.Skipf("IPv6 unavailable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	endpoint := publicEndpoint{
		IP:   "2001:db8::40",
		Port: conn.LocalAddr().(*net.UDPAddr).Port,
		Conn: conn,
	}

	got, publicKnown := srv.mapPortsWithUPnP(context.Background(), endpoint, true)
	if !publicKnown || got.IP != endpoint.IP || got.Port != endpoint.Port {
		t.Fatalf("IPv6 endpoint changed by IPv4 NAT mapping: got %+v", got)
	}
	if factoryCalls.Load() != 0 {
		t.Fatal("IPv4 NAT mapper was created for an IPv6 endpoint")
	}
}

func TestRefreshPublicUDPFromMappingFormatsIPv6(t *testing.T) {
	t.Parallel()

	srv := newNetworkFindingServer(t, &testutil.MockPeerClient{})
	srv.publicUDPAddr = net.JoinHostPort("2001:db8::30", "40000")
	srv.refreshPublicUDPFromMapping("2001:db8::30", 45000)

	if got, want := srv.CurrentNATState().PublicUDPAddr, net.JoinHostPort("2001:db8::30", "45000"); got != want {
		t.Fatalf("public UDP address = %q, want %q", got, want)
	}
}
