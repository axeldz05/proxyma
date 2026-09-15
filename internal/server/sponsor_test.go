package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func sponsorRecord(addr string, isSponsor bool) protocol.AddressRecord {
	return protocol.AddressRecord{Addresses: []string{addr}, IsSponsor: isSponsor}
}

func newSponsorTestServer(t *testing.T) *Server {
	t.Helper()
	logger := protocol.NewLogger(testutil.TestLogWriter{T: t}, false)
	srv := &Server{
		Config: protocol.NodeConfig{ID: "self", Logger: logger},
		Peers:  NewPeerRegistry(logger, "self"),
	}
	srv.Relays = NewRelayManager(srv)
	return srv
}

func TestFirstOnlineSponsorSelector(t *testing.T) {
	t.Parallel()

	selector := FirstOnlineSponsorSelector{}
	cands := []SponsorCandidate{
		{ID: "c", Addr: "https://c:8443", Online: false},
		{ID: "a", Addr: "https://a:8443", Online: true},
		{ID: "b", Addr: "https://b:8443", Online: true},
		{ID: "d", Addr: "", Online: true}, // no primary address, unusable
	}

	t.Run("deterministic lowest online ID regardless of input order", func(t *testing.T) {
		for range 5 {
			cand, ok := selector.Select(cands, "")
			require.True(t, ok)
			require.Equal(t, "a", cand.ID)
			require.Equal(t, "https://a:8443", cand.Addr)
		}
	})

	t.Run("never returns the current sponsor", func(t *testing.T) {
		cand, ok := selector.Select(cands, "https://a:8443")
		require.True(t, ok)
		require.Equal(t, "b", cand.ID)
	})

	t.Run("prefers online over offline", func(t *testing.T) {
		onlyOffline := []SponsorCandidate{
			{ID: "x", Addr: "https://x:8443", Online: false},
			{ID: "y", Addr: "https://y:8443", Online: false},
		}
		// Offline fallback is allowed when nothing is online (matches the
		// pre-existing relay failover behavior).
		cand, ok := selector.Select(onlyOffline, "")
		require.True(t, ok)
		require.Equal(t, "x", cand.ID)

		mixed := append(onlyOffline, SponsorCandidate{ID: "z", Addr: "https://z:8443", Online: true})
		cand, ok = selector.Select(mixed, "")
		require.True(t, ok)
		require.Equal(t, "z", cand.ID)
	})

	t.Run("offline fallback only when nothing is online", func(t *testing.T) {
		offline := []SponsorCandidate{
			{ID: "x", Addr: "https://x:8443", Online: false},
			{ID: "y", Addr: "https://y:8443", Online: false},
		}
		cand, ok := selector.Select(offline, "https://x:8443")
		require.True(t, ok)
		require.Equal(t, "y", cand.ID)

		cand, ok = selector.Select(offline, "https://x:8443")
		require.True(t, ok)
		require.Equal(t, "y", cand.ID)
	})

	t.Run("empty candidates fail", func(t *testing.T) {
		_, ok := selector.Select(nil, "")
		require.False(t, ok)
	})
}

func TestSelectSponsorFromRegistry(t *testing.T) {
	t.Parallel()

	srv := newSponsorTestServer(t)
	_, _ = srv.Peers.AddPeer("s-b", sponsorRecord("https://b:8443", true))
	_, _ = srv.Peers.AddPeer("s-a", sponsorRecord("https://a:8443", true))
	_, _ = srv.Peers.AddPeer("plain", sponsorRecord("https://plain:8443", false))
	// AddPeer marks peers online optimistically; make the offline states
	// explicit (mirrors startup marking persisted peers "not contacted yet").
	srv.Peers.SetPeerOffline("s-a", nil)
	srv.Peers.SetPeerOffline("plain", nil)

	addr, ok := srv.selectSponsor("")
	require.True(t, ok)
	require.Equal(t, "https://b:8443", addr)

	// Registry snapshot stays the SSOT: a demoted sponsor stops being preferred
	// and the remaining offline sponsor-capable peer becomes the fallback.
	_, _ = srv.Peers.AddPeer("s-b", sponsorRecord("https://b:8443", false))
	addr, ok = srv.selectSponsor("")
	require.True(t, ok)
	require.Equal(t, "https://a:8443", addr)

	// With no sponsor-capable peers left there is nothing to select.
	_, _ = srv.Peers.AddPeer("s-a", sponsorRecord("https://a:8443", false))
	_, ok = srv.selectSponsor("")
	require.False(t, ok)
}

// sponsorTrackingClient records sponsor changes on top of the no-op mock.
// Updates come from the polling goroutine, so the field is mutex-guarded.
type sponsorTrackingClient struct {
	*testutil.MockPeerClient
	mu      sync.Mutex
	sponsor string
}

func (c *sponsorTrackingClient) UpdateSponsorAddress(addr string) {
	c.mu.Lock()
	c.sponsor = addr
	c.mu.Unlock()
}

func (c *sponsorTrackingClient) CurrentSponsorAddress() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sponsor
}

// Sponsor reads the current recorded value from test assertions.
func (c *sponsorTrackingClient) Sponsor() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sponsor
}

func TestReconcileSponsor(t *testing.T) {
	t.Parallel()

	srv := newSponsorTestServer(t)
	tracker := &sponsorTrackingClient{MockPeerClient: &testutil.MockPeerClient{}}
	srv.peerClient = tracker

	_, _ = srv.Peers.AddPeer("s-a", sponsorRecord("https://a:8443", true))
	_, _ = srv.Peers.AddPeer("s-b", sponsorRecord("https://b:8443", true))
	srv.Peers.SetPeerOffline("s-a", nil)
	srv.Peers.SetPeerOnline("s-b", true)

	t.Run("adopts when no sponsor is set", func(t *testing.T) {
		srv.reconcileSponsor()
		require.Equal(t, "https://b:8443", tracker.Sponsor())
	})

	t.Run("keeps a healthy capable sponsor", func(t *testing.T) {
		tracker.UpdateSponsorAddress("https://a:8443")
		srv.Peers.SetPeerOnline("s-a", true)
		srv.reconcileSponsor()
		require.Equal(t, "https://a:8443", tracker.Sponsor())
	})

	t.Run("reselects when the current sponsor loses capability", func(t *testing.T) {
		_, _ = srv.Peers.AddPeer("s-a", sponsorRecord("https://a:8443", false))
		srv.reconcileSponsor()
		require.Equal(t, "https://b:8443", tracker.Sponsor())
	})

	t.Run("keeps an incapable sponsor when no alternative exists", func(t *testing.T) {
		_, _ = srv.Peers.AddPeer("s-b", sponsorRecord("https://b:8443", false))
		tracker.UpdateSponsorAddress("https://b:8443")
		srv.reconcileSponsor()
		require.Equal(t, "https://b:8443", tracker.Sponsor())
	})
}

// TestStartupAdoptsSponsorWithoutBootstrap seeds persisted peers into storage
// and starts a node without bootstrap_node: the sponsor must be adopted from
// the persisted topology during New.
func TestStartupAdoptsSponsorWithoutBootstrap(t *testing.T) {
	t.Parallel()

	cfg := testutil.DefaultConfig(t, "adopt-node") // no BootstrapNode set
	engine := testutil.NewStorageEngine(t, cfg)
	require.NoError(t, engine.SavePeer("s-a", sponsorRecord("https://a:8443", true)))
	_ = engine.SavePeer("plain", sponsorRecord("https://plain:8443", false))
	require.NoError(t, engine.Close())

	tracker := &sponsorTrackingClient{MockPeerClient: &testutil.MockPeerClient{}}
	app, err := New(cfg, tracker)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	require.Equal(t, "https://a:8443", tracker.Sponsor())
}

// TestRelayFailoverDeterministic drives the polling loop against a dead
// sponsor: after the poll failure it must switch to the lowest-ID online
// sponsor candidate from the registry.
func TestRelayFailoverDeterministic(t *testing.T) {
	t.Parallel()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(dead.Close)

	srv := newSponsorTestServer(t)
	tracker := &sponsorTrackingClient{MockPeerClient: &testutil.MockPeerClient{}}
	tracker.MockPeerClient.OnPollRelay = func(context.Context, string, string) (protocol.RelayRequest, error) {
		return protocol.RelayRequest{}, context.DeadlineExceeded
	}
	tracker.UpdateSponsorAddress(dead.URL)
	srv.peerClient = tracker
	srv.Config.MinRelayPollInterval = 1

	// The offline candidate has the lower ID; the online one must win.
	_, _ = srv.Peers.AddPeer("s-m", sponsorRecord("https://m:8443", true))
	_, _ = srv.Peers.AddPeer("s-z", sponsorRecord("https://z:8443", true))
	srv.Peers.SetPeerOffline("s-m", nil)
	srv.Peers.SetPeerOnline("s-z", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.StartRelayPolling(ctx, dead.URL)

	require.Eventually(t, func() bool {
		return tracker.Sponsor() == "https://z:8443"
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
}
