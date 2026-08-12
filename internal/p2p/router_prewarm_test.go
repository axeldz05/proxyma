package p2p_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

type prewarmRoundTripper func(*http.Request) (*http.Response, error)

func (f prewarmRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemovePeerRouteCancelsDetachedPrewarm(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	t.Cleanup(func() { _ = udp.Close() })
	qm := p2p.NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	started := make(chan struct{})
	canceled := make(chan struct{})
	router := &p2p.P2PRoundTripper{
		QM:             qm,
		SponsorAddress: "https://sponsor.invalid",
		Base: prewarmRoundTripper(func(req *http.Request) (*http.Response, error) {
			close(started)
			<-req.Context().Done()
			close(canceled)
			return nil, req.Context().Err()
		}),
	}
	router.UpdatePeerRoute("remote", protocol.AddressRecord{
		Addresses: []string{p2p.FormatQUICAddr("127.0.0.1:9")},
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prewarm relay attempt did not start")
	}
	router.RemovePeerRoute("remote")

	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("removing a peer did not cancel its detached prewarm")
	}
	require.Eventually(t, func() bool {
		_, exists := qm.GetSession("remote")
		return !exists
	}, time.Second, 10*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://remote.proxyma.local/health", nil)
	require.NoError(t, err)
	_, err = router.RoundTrip(req)
	require.ErrorContains(t, err, "unknown peer")
}

func TestRouterCloseCancelsAndJoinsPrewarm(t *testing.T) {
	t.Parallel()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	qm := p2p.NewQUICManager("local", udp, nil, nil, nil, nil)
	t.Cleanup(qm.Close)

	started := make(chan struct{})
	canceled := make(chan struct{})
	router := &p2p.P2PRoundTripper{
		QM:             qm,
		SponsorAddress: "https://sponsor.invalid",
		Base: prewarmRoundTripper(func(req *http.Request) (*http.Response, error) {
			close(started)
			<-req.Context().Done()
			close(canceled)
			return nil, req.Context().Err()
		}),
	}
	router.UpdatePeerRoute("remote", protocol.AddressRecord{
		Addresses: []string{"quic://127.0.0.1:9999"},
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prewarm did not start")
	}

	closed := make(chan struct{})
	go func() {
		router.Close()
		close(closed)
	}()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("router close did not cancel prewarm")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("router close returned without joining prewarm")
	}
}
