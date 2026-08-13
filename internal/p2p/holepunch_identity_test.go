package p2p

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type closeCountingPacketConn struct {
	net.PacketConn
	closes atomic.Int32
}

func (c *closeCountingPacketConn) Close() error {
	c.closes.Add(1)
	return c.PacketConn.Close()
}

func TestHolePunchRejectsResponseFromUnexpectedSender(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	t.Cleanup(func() { _ = udp.Close() })
	qm := NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	_, err = qm.performHolePunch(
		context.Background(),
		"expected-peer",
		[]string{FormatQUICAddr("127.0.0.1:9")},
		func(targetPeer, path string, body []byte) ([]byte, error) {
			return json.Marshal(HolePunchMessage{
				SenderID:  "different-peer",
				PublicUDP: "malformed-address-that-must-not-be-used",
			})
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sender")
	require.Contains(t, err.Error(), "expected-peer")
}

func TestHolePunchRejectsRemoteAddressFromDifferentSocketFamily(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	t.Cleanup(func() { _ = udp.Close() })
	qm := NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = qm.performHolePunch(
		ctx,
		"expected-peer",
		[]string{FormatQUICAddr("[::1]:9")},
		func(targetPeer, path string, body []byte) ([]byte, error) {
			return json.Marshal(HolePunchMessage{
				SenderID:  "expected-peer",
				PublicUDP: "[::1]:9",
			})
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "address family")
}

func TestQUICManagerCloseClosesCallerPacketConnExactlyOnce(t *testing.T) {
	t.Parallel()

	raw, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	counting := &closeCountingPacketConn{PacketConn: raw}
	qm := newQUICManagerWithPacketConn("local", counting, nil, nil, nil, nil)

	qm.Close()
	qm.Close()
	require.Equal(t, int32(1), counting.closes.Load())
}

func TestCompatibleQUICAddressesSkipsWrongFamilyBeforeUsableAddress(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	qm := NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	addresses, err := qm.compatibleQUICAddresses([]string{
		"quic://[2001:db8::1]:41000",
		"quic://127.0.0.1:42000",
	})
	require.NoError(t, err)
	require.Len(t, addresses, 1)
	require.Equal(t, "127.0.0.1:42000", addresses[0].String())
}

func TestNewHolePunchGenerationRetriesCanceledPredecessor(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	qm := NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	var relayCalls atomic.Int32
	sendRelay := func(string, string, []byte) ([]byte, error) {
		switch relayCalls.Add(1) {
		case 1:
			close(firstStarted)
		case 2:
			close(secondStarted)
		}
		return json.Marshal(HolePunchMessage{
			SenderID:  "remote",
			PublicUDP: "127.0.0.1:9",
		})
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, punchErr := qm.InitiateHolePunch(firstCtx, "remote", []string{"quic://127.0.0.1:9"}, sendRelay)
		firstDone <- punchErr
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first hole-punch generation did not start")
	}

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, punchErr := qm.InitiateHolePunch(secondCtx, "remote", []string{"quic://127.0.0.1:9"}, sendRelay)
		secondDone <- punchErr
	}()
	cancelFirst()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("canceled predecessor did not finish")
	}
	select {
	case <-secondStarted:
	case err := <-secondDone:
		t.Fatalf("replacement inherited predecessor cancellation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("replacement generation did not retry")
	}
	cancelSecond()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("replacement generation did not honor cancellation")
	}
}
