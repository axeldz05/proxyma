package p2p

import (
	"net"
	"testing"

	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"
)

func TestAllocateFamilyLocked(t *testing.T) {
	t.Parallel()

	v4Server := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}
	v6Server := &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 3478}
	v4Peer := net.IPv4(192, 0, 2, 10)
	v6Peer := net.ParseIP("2001:db8::10")

	t.Run("defaults to IPv4 like before dual-stack", func(t *testing.T) {
		tm := &TURNManager{serverV4: v4Server, serverV6: v6Server}
		network, addr, family, err := tm.allocateFamilyLocked()
		require.NoError(t, err)
		require.Equal(t, "udp4", network)
		require.Equal(t, v4Server, addr)
		require.Equal(t, turn.RequestedAddressFamilyIPv4, family)
	})

	t.Run("picks IPv6 when all desired peers are IPv6", func(t *testing.T) {
		tm := &TURNManager{
			serverV4:   v4Server,
			serverV6:   v6Server,
			desiredIPs: map[string]net.IP{"2001:db8::10": v6Peer},
		}
		network, addr, family, err := tm.allocateFamilyLocked()
		require.NoError(t, err)
		require.Equal(t, "udp6", network)
		require.Equal(t, v6Server, addr)
		require.Equal(t, turn.RequestedAddressFamilyIPv6, family)
	})

	t.Run("mixed desired peers keep IPv4", func(t *testing.T) {
		tm := &TURNManager{
			serverV4:   v4Server,
			serverV6:   v6Server,
			desiredIPs: map[string]net.IP{"2001:db8::10": v6Peer, "192.0.2.10": v4Peer},
		}
		network, _, _, err := tm.allocateFamilyLocked()
		require.NoError(t, err)
		require.Equal(t, "udp4", network)
	})

	t.Run("falls back to IPv6 when the server has no IPv4", func(t *testing.T) {
		tm := &TURNManager{serverV6: v6Server}
		network, addr, family, err := tm.allocateFamilyLocked()
		require.NoError(t, err)
		require.Equal(t, "udp6", network)
		require.Equal(t, v6Server, addr)
		require.Equal(t, turn.RequestedAddressFamilyIPv6, family)
	})

	t.Run("errors when the server resolves to nothing", func(t *testing.T) {
		tm := &TURNManager{}
		_, _, _, err := tm.allocateFamilyLocked()
		require.Error(t, err)
	})
}

func TestSameFamily(t *testing.T) {
	t.Parallel()

	require.True(t, sameFamily(net.IPv4(192, 0, 2, 1), "udp4"))
	require.False(t, sameFamily(net.ParseIP("2001:db8::1"), "udp4"))
	require.True(t, sameFamily(net.ParseIP("2001:db8::1"), "udp6"))
	require.False(t, sameFamily(net.IPv4(192, 0, 2, 1), "udp6"))
}
