package p2p

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func issueQUICNodeTLS(t *testing.T, caDir, nodeDir, nodeID string) (serverTLS, clientTLS *tls.Config) {
	t.Helper()
	require.NoError(t, IssueNodeCertificate(caDir, nodeDir, nodeID))
	caPath, _ := CACertPaths(caDir)
	certPath, keyPath := NodeCertPaths(nodeDir, nodeID)
	serverTLS, clientTLS, err := LoadNodeTLS(caPath, certPath, keyPath)
	require.NoError(t, err)
	return serverTLS, clientTLS
}

func newQUICManagerForTest(
	t *testing.T,
	id string,
	clientTLS, serverTLS *tls.Config,
) *QUICManager {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = udp.Close() })
	qm := NewQUICManager(id, udp, clientTLS, serverTLS, http.NotFoundHandler(), nil)
	t.Cleanup(qm.Close)
	return qm
}

func TestReloadTLSRejectsInFlightDialAndInvalidatesSessions(t *testing.T) {
	t.Parallel()

	caDir := t.TempDir()
	require.NoError(t, InitCluster(caDir))
	serverTLS, serverClientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "zebra")
	clientServerTLS, clientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "apple")

	serverBase := serverTLS.Clone()
	serverBase.NextProtos = []string{quicALPN}
	var blockHandshake atomic.Bool
	handshakeStarted := make(chan struct{}, 1)
	releaseHandshake := make(chan struct{})
	var releaseOnce sync.Once

	blockingServerTLS := serverBase.Clone()
	blockingServerTLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		if blockHandshake.Load() {
			select {
			case handshakeStarted <- struct{}{}:
			default:
			}
			<-releaseHandshake
		}
		return serverBase.Clone(), nil
	}

	serverQM := newQUICManagerForTest(t, "zebra", serverClientTLS, blockingServerTLS)
	require.NoError(t, serverQM.StartListener())
	clientQM := newQUICManagerForTest(t, "apple", clientTLS, clientServerTLS)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseHandshake) }) })
	serverAddr := serverQM.PacketConn.LocalAddr().(*net.UDPAddr)

	initialCtx, initialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	initialConn, err := clientQM.establishSessionAfterPunch(initialCtx, "zebra", serverAddr)
	initialCancel()
	require.NoError(t, err)
	require.NotNil(t, initialConn)
	_, exists := clientQM.GetSession("zebra")
	require.True(t, exists)

	blockHandshake.Store(true)
	type dialOutcome struct {
		conn *quic.Conn
		err  error
	}
	outcomeCh := make(chan dialOutcome, 1)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	go func() {
		conn, dialErr := clientQM.establishSessionAfterPunch(dialCtx, "zebra", serverAddr)
		outcomeCh <- dialOutcome{conn: conn, err: dialErr}
	}()

	select {
	case <-handshakeStarted:
	case <-dialCtx.Done():
		t.Fatal("second QUIC handshake did not reach the server")
	}

	clientQM.ReloadTLS(clientTLS, clientServerTLS)
	_, exists = clientQM.GetSession("zebra")
	require.False(t, exists, "ReloadTLS must invalidate established sessions")
	releaseOnce.Do(func() { close(releaseHandshake) })

	var outcome dialOutcome
	select {
	case outcome = <-outcomeCh:
	case <-dialCtx.Done():
		t.Fatal("in-flight QUIC dial did not finish")
	}
	if outcome.conn != nil {
		t.Cleanup(func() { _ = outcome.conn.CloseWithError(0, "test complete") })
	}
	require.Error(t, outcome.err, "a dial using pre-rotation TLS must not repopulate the session map")
	_, exists = clientQM.GetSession("zebra")
	require.False(t, exists, "a pre-rotation in-flight dial must remain invalidated")
}

func TestReloadTLSConcurrentWithListenerAndDial(t *testing.T) {
	t.Parallel()

	caDir := t.TempDir()
	require.NoError(t, InitCluster(caDir))
	remoteServerTLS, remoteClientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "zebra")
	localServerTLS, localClientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "apple")

	remoteQM := newQUICManagerForTest(t, "zebra", remoteClientTLS, remoteServerTLS)
	require.NoError(t, remoteQM.StartListener())
	localQM := newQUICManagerForTest(t, "apple", localClientTLS, localServerTLS)
	remoteAddr := remoteQM.PacketConn.LocalAddr().(*net.UDPAddr)

	start := make(chan struct{})
	listenErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		listenErr <- localQM.StartListener()
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 64 {
			localQM.ReloadTLS(localClientTLS, localServerTLS)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 8 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			conn, _ := localQM.establishSessionAfterPunch(ctx, "zebra", remoteAddr)
			cancel()
			if conn != nil {
				_ = conn.CloseWithError(0, "next dial")
			}
		}
	}()

	close(start)
	wg.Wait()
	require.NoError(t, <-listenErr)
}

func TestReloadTLSRejectsInboundHandshakeStartedBeforeRotation(t *testing.T) {
	t.Parallel()

	caDir := t.TempDir()
	require.NoError(t, InitCluster(caDir))
	oldServerTLS, serverClientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "zebra")
	_, clientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "apple")
	newServerTLS, newServerClientTLS := issueQUICNodeTLS(t, caDir, t.TempDir(), "zebra")

	oldServerBase := oldServerTLS.Clone()
	oldServerBase.NextProtos = []string{quicALPN}
	handshakeStarted := make(chan struct{})
	releaseHandshake := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	blockingServerTLS := oldServerBase.Clone()
	blockingServerTLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		startOnce.Do(func() { close(handshakeStarted) })
		<-releaseHandshake
		return oldServerBase.Clone(), nil
	}

	serverQM := newQUICManagerForTest(t, "zebra", serverClientTLS, blockingServerTLS)
	require.NoError(t, serverQM.StartListener())
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseHandshake) }) })

	dialTLS := clientTLS.Clone()
	dialTLS.NextProtos = []string{quicALPN}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type inboundOutcome struct {
		conn *quic.Conn
		err  error
	}
	dialResult := make(chan inboundOutcome, 1)
	go func() {
		conn, err := quic.DialAddr(ctx, serverQM.PacketConn.LocalAddr().String(), dialTLS, &quic.Config{})
		dialResult <- inboundOutcome{conn: conn, err: err}
	}()

	select {
	case <-handshakeStarted:
	case <-ctx.Done():
		t.Fatal("inbound handshake did not reach the server callback")
	}
	serverQM.ReloadTLS(newServerClientTLS, newServerTLS)
	releaseOnce.Do(func() { close(releaseHandshake) })

	var outcome inboundOutcome
	select {
	case outcome = <-dialResult:
	case <-ctx.Done():
		t.Fatal("inbound handshake did not finish after release")
	}
	if outcome.err == nil {
		select {
		case <-outcome.conn.Context().Done():
		case <-time.After(500 * time.Millisecond):
			_ = outcome.conn.CloseWithError(0, "test cleanup")
			t.Fatal("a handshake that selected pre-rotation TLS remained active")
		}
	}
	_, exists := serverQM.GetSession("apple")
	require.False(t, exists, "pre-rotation inbound handshake must not repopulate sessions")
}

func TestReloadTLSRotatesStandaloneListenerWithoutCallback(t *testing.T) {
	t.Parallel()

	oldCADir := t.TempDir()
	require.NoError(t, InitCluster(oldCADir))
	oldServerTLS, oldServerClientTLS := issueQUICNodeTLS(t, oldCADir, t.TempDir(), "zebra")
	_, oldClientTLS := issueQUICNodeTLS(t, oldCADir, t.TempDir(), "apple")

	newCADir := t.TempDir()
	require.NoError(t, InitCluster(newCADir))
	newServerTLS, newServerClientTLS := issueQUICNodeTLS(t, newCADir, t.TempDir(), "zebra")
	_, newClientTLS := issueQUICNodeTLS(t, newCADir, t.TempDir(), "apple")

	serverQM := newQUICManagerForTest(t, "zebra", oldServerClientTLS, oldServerTLS)
	require.Nil(t, oldServerTLS.GetConfigForClient)
	require.NoError(t, serverQM.StartListener())
	listenerBefore := serverQM.QUICListener

	serverQM.ReloadTLS(newServerClientTLS, newServerTLS)
	require.Same(t, listenerBefore, serverQM.QUICListener)

	currentClientTLS := newClientTLS.Clone()
	currentClientTLS.NextProtos = []string{quicALPN}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, serverQM.PacketConn.LocalAddr().String(), currentClientTLS, &quic.Config{})
	require.NoError(t, err, "standalone listener must serve reloaded TLS without an external callback")
	require.Equal(t, newServerTLS.Certificates[0].Certificate[0], conn.ConnectionState().TLS.PeerCertificates[0].Raw)
	_ = conn.CloseWithError(0, "test complete")

	staleClientTLS := oldClientTLS.Clone()
	staleClientTLS.NextProtos = []string{quicALPN}
	staleCtx, staleCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer staleCancel()
	staleConn, staleErr := quic.DialAddr(staleCtx, serverQM.PacketConn.LocalAddr().String(), staleClientTLS, &quic.Config{})
	if staleConn != nil {
		_ = staleConn.CloseWithError(0, "stale client")
	}
	require.Error(t, staleErr, "listener must not keep presenting its pre-rotation certificate")
}
