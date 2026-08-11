package p2p_test

import (
	"net"
	"proxyma/internal/p2p"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHolePunchPingRoundTrip(t *testing.T) {
	t.Parallel()
	payload := p2p.HolePunchPingPayload("node-a")
	id, ok := p2p.ParseHolePunchPing(payload)
	require.True(t, ok)
	require.Equal(t, "node-a", id)
}

func TestParseHolePunchPingRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, ok := p2p.ParseHolePunchPing([]byte("hello"))
	require.False(t, ok)
	_, ok = p2p.ParseHolePunchPing([]byte{0xff, 0xff, 0xff, 0xff, 'x'})
	require.False(t, ok)
	_, ok = p2p.ParseHolePunchPing(nil)
	require.False(t, ok)
}

func TestBurstPingsOverLocalUDP(t *testing.T) {
	t.Parallel()

	recv, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = recv.Close() })

	send, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = send.Close() })

	addr := recv.LocalAddr().(*net.UDPAddr)
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		_ = recv.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := recv.ReadFrom(buf)
		if err != nil {
			return
		}
		if id, ok := p2p.ParseHolePunchPing(buf[:n]); ok {
			done <- id
		}
	}()

	p2p.BurstPings(send, addr, "puncher", 3, 0)

	select {
	case id := <-done:
		require.Equal(t, "puncher", id)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive hole-punch ping")
	}
}

func TestHolePunchPacketConnInterceptsPings(t *testing.T) {
	t.Parallel()

	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	wrapped := p2p.NewHolePunchPacketConn(raw)

	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	go func() {
		_ = wrapped.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		// Should block until a non-ping packet arrives; pings are intercepted.
		_, _, _ = wrapped.ReadFrom(buf)
	}()

	dst := raw.LocalAddr().(*net.UDPAddr)
	_, err = peer.WriteTo(p2p.HolePunchPingPayload("peer-z"), dst)
	require.NoError(t, err)

	select {
	case id := <-wrapped.PingCh:
		require.Equal(t, "peer-z", id)
	case <-time.After(2 * time.Second):
		t.Fatal("PingCh did not receive intercepted sender")
	}
}

func TestBurstPingsNoopOnNil(t *testing.T) {
	t.Parallel()
	p2p.BurstPings(nil, nil, "x", 5, time.Millisecond)
	p2p.BurstPings(nil, &net.UDPAddr{}, "x", 0, 0)
}

func TestHolePunchPingDemuxDoesNotSteal(t *testing.T) {
	t.Parallel()

	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	wrapped := p2p.NewHolePunchPacketConn(raw)
	waitA := wrapped.RegisterPingWait("peer-a")
	waitB := wrapped.RegisterPingWait("peer-b")
	defer wrapped.UnregisterPingWait("peer-a")
	defer wrapped.UnregisterPingWait("peer-b")

	go func() {
		_ = wrapped.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		_, _, _ = wrapped.ReadFrom(buf)
	}()

	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	dst := raw.LocalAddr().(*net.UDPAddr)

	_, err = peer.WriteTo(p2p.HolePunchPingPayload("peer-b"), dst)
	require.NoError(t, err)

	select {
	case <-waitB:
	case <-time.After(2 * time.Second):
		t.Fatal("peer-b waiter did not receive its ping")
	}
	select {
	case <-waitA:
		t.Fatal("peer-a waiter must not be signaled by peer-b ping")
	case <-time.After(50 * time.Millisecond):
	}
}
