package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"proxyma/internal/protocol"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// HolePunchMessage is the payload exchanged via the Sponsor relay
type HolePunchMessage struct {
	SenderID  string `json:"sender_id"`
	PublicUDP string `json:"public_udp"`
}

func requireMatchingUDPFamily(conn net.PacketConn, remote *net.UDPAddr) error {
	if conn == nil || remote == nil {
		return fmt.Errorf("cannot validate UDP address family without local and remote endpoints")
	}
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || remote.IP == nil {
		return fmt.Errorf("cannot determine UDP address family for local %v and remote %v", conn.LocalAddr(), remote)
	}
	localIPv4 := local.IP.To4() != nil
	remoteIPv4 := remote.IP.To4() != nil
	if localIPv4 != remoteIPv4 {
		return fmt.Errorf("UDP address family mismatch: local %s, remote %s", local.String(), remote.String())
	}
	return nil
}

var holePunchMagic = []byte{0xff, 0xff, 0xff, 0xff}

// HolePunchPingPayload builds a UDP hole-punch ping for localID.
func HolePunchPingPayload(localID string) []byte {
	return append(append([]byte{}, holePunchMagic...), []byte("ping:"+localID)...)
}

// ParseHolePunchPing extracts the sender ID from a hole-punch ping packet.
func ParseHolePunchPing(p []byte) (senderID string, ok bool) {
	if len(p) < 4 || !bytes.Equal(p[:4], holePunchMagic) {
		return "", false
	}
	payload := string(p[4:])
	if !strings.HasPrefix(payload, "ping:") {
		return "", false
	}
	return strings.TrimPrefix(payload, "ping:"), true
}

func defaultQUICConfig() *quic.Config {
	return &quic.Config{KeepAlivePeriod: 15 * time.Second}
}

const quicALPN = "proxyma-p2p"
const quicGenerationExporter = "proxyma-quic-tls-generation"

func cloneQUICTLSConfig(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return nil
	}
	clone := cfg.Clone()
	clone.NextProtos = []string{quicALPN}
	return clone
}

// BurstPings sends n hole-punch pings to addr at the given interval (L2).
func BurstPings(pc net.PacketConn, addr *net.UDPAddr, localID string, n int, interval time.Duration) {
	if pc == nil || addr == nil || n <= 0 {
		return
	}
	pingPayload := HolePunchPingPayload(localID)
	for i := 0; i < n; i++ {
		_, _ = pc.WriteTo(pingPayload, addr)
		if i+1 < n && interval > 0 {
			time.Sleep(interval)
		}
	}
}

func (qm *QUICManager) serveIncomingStreams(conn *quic.Conn, tlsState *tls.ConnectionState) {
	for {
		stream, err := conn.AcceptStream(qm.lifetime())
		if err != nil {
			return
		}
		go qm.handleIncomingStream(stream, tlsState)
	}
}

// HolePunchPacketConn wraps net.PacketConn to intercept hole punching pings
type HolePunchPacketConn struct {
	net.PacketConn
	PingCh chan string // receives sender IDs of successful pings (tests / observers)

	waitMu  sync.Mutex
	waiters map[string]chan struct{} // peerID -> buffered signal for demuxed waits
}

func NewHolePunchPacketConn(pc net.PacketConn) *HolePunchPacketConn {
	return &HolePunchPacketConn{
		PacketConn: pc,
		PingCh:     make(chan string, 100),
		waiters:    make(map[string]chan struct{}),
	}
}

// RegisterPingWait returns a channel closed/signaled when a ping from peerID arrives.
// Caller must UnregisterPingWait when done.
func (h *HolePunchPacketConn) RegisterPingWait(peerID string) <-chan struct{} {
	ch := make(chan struct{}, 1)
	h.waitMu.Lock()
	h.waiters[peerID] = ch
	h.waitMu.Unlock()
	return ch
}

func (h *HolePunchPacketConn) UnregisterPingWait(peerID string) {
	h.waitMu.Lock()
	delete(h.waiters, peerID)
	h.waitMu.Unlock()
}

func (h *HolePunchPacketConn) notifyPing(senderID string) {
	h.waitMu.Lock()
	if ch, ok := h.waiters[senderID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.waitMu.Unlock()
	select {
	case h.PingCh <- senderID:
	default:
	}
}

func (h *HolePunchPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	for {
		n, addr, err = h.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}

		// Intercept hole punch pings (prefix: 4 bytes of 0xff)
		if senderID, ok := ParseHolePunchPing(p[:n]); ok {
			h.notifyPing(senderID)
			continue // Intercepted, read next packet
		}
		return n, addr, nil
	}
}

type dialResult struct {
	done chan struct{}
	conn *quic.Conn
	err  error
}

// Logger is the logging surface the QUIC manager needs (satisfied by *slog.Logger).
type Logger interface {
	Info(msg string, args ...any)
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// QUICManager manages active direct QUIC sessions and incoming listeners
type QUICManager struct {
	LocalID       string
	publicUDPAddr string
	PacketConn    *HolePunchPacketConn
	QUICListener  *quic.Listener
	Transport     *quic.Transport
	Sessions      map[string]*quic.Conn
	blockedPeers  map[string]struct{}
	SessionsMu    sync.RWMutex
	TLSClient     *tls.Config
	TLSServer     *tls.Config
	HTTPHandler   http.Handler
	Logger        Logger
	publicMu      sync.RWMutex

	// tlsMu protects TLS config publication and its generation. Configs are
	// immutable after publication, so dial/listen operations may retain a
	// snapshot after releasing the read lock.
	tlsMu         sync.RWMutex
	tlsGeneration uint64
	listenerMu    sync.Mutex
	handshakeGen  sync.Map // TLS exporter key -> uint64 generation

	// ctx is cancelled by Close so accept loops unblock at shutdown instead of
	// waiting on their own connection teardown.
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	dialsMu sync.Mutex
	dials   map[string]*dialResult
}

func NewQUICManager(localID string, conn *net.UDPConn, clientTLS, serverTLS *tls.Config, handler http.Handler, logger Logger) *QUICManager {
	return newQUICManagerWithPacketConn(localID, conn, clientTLS, serverTLS, handler, logger)
}

func newQUICManagerWithPacketConn(localID string, conn net.PacketConn, clientTLS, serverTLS *tls.Config, handler http.Handler, logger Logger) *QUICManager {
	wrapped := NewHolePunchPacketConn(conn)

	// Clone TLS configs and append NextProtos required by QUIC
	clTls := cloneQUICTLSConfig(clientTLS)
	srvTls := cloneQUICTLSConfig(serverTLS)

	transport := &quic.Transport{
		Conn: wrapped,
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &QUICManager{
		LocalID:       localID,
		PacketConn:    wrapped,
		Transport:     transport,
		Sessions:      make(map[string]*quic.Conn),
		blockedPeers:  make(map[string]struct{}),
		TLSClient:     clTls,
		TLSServer:     srvTls,
		HTTPHandler:   handler,
		Logger:        logger,
		tlsGeneration: 1,
		ctx:           ctx,
		cancel:        cancel,
		dials:         make(map[string]*dialResult),
	}
}

func (qm *QUICManager) clientTLSSnapshot() (*tls.Config, uint64) {
	qm.tlsMu.RLock()
	defer qm.tlsMu.RUnlock()
	return qm.TLSClient, qm.tlsGeneration
}

func (qm *QUICManager) SetPublicUDPAddr(addr string) {
	qm.publicMu.Lock()
	qm.publicUDPAddr = addr
	qm.publicMu.Unlock()
}

func (qm *QUICManager) PublicUDPAddress() string {
	qm.publicMu.RLock()
	defer qm.publicMu.RUnlock()
	return qm.publicUDPAddr
}

func (qm *QUICManager) serverTLSSnapshot() (*tls.Config, uint64) {
	qm.tlsMu.RLock()
	defer qm.tlsMu.RUnlock()
	return qm.TLSServer, qm.tlsGeneration
}

func tlsConnectionGenerationKey(state *tls.ConnectionState) (string, error) {
	key, err := state.ExportKeyingMaterial(quicGenerationExporter, nil, 32)
	if err != nil {
		return "", fmt.Errorf("export QUIC TLS generation key: %w", err)
	}
	return string(key), nil
}

func (qm *QUICManager) serverConfigForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	base, generation := qm.serverTLSSnapshot()
	if base == nil {
		return nil, fmt.Errorf("QUIC server TLS is not configured")
	}

	cfg := base.Clone()
	upstream := cfg.GetConfigForClient
	cfg.GetConfigForClient = nil
	if upstream != nil {
		selected, err := upstream(hello)
		if err != nil {
			return nil, err
		}
		if selected != nil {
			cfg = selected.Clone()
		}
	}

	cfg.GetConfigForClient = nil
	cfg.NextProtos = []string{quicALPN}
	verifyConnection := cfg.VerifyConnection
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if verifyConnection != nil {
			if err := verifyConnection(state); err != nil {
				return err
			}
		}

		qm.tlsMu.RLock()
		current := qm.tlsGeneration == generation
		qm.tlsMu.RUnlock()
		if !current {
			return fmt.Errorf("QUIC TLS rotated during inbound handshake")
		}
		key, err := tlsConnectionGenerationKey(&state)
		if err != nil {
			return err
		}
		qm.handshakeGen.Store(key, generation)
		return nil
	}
	return cfg, nil
}

func (qm *QUICManager) consumeHandshakeGeneration(state *tls.ConnectionState) (uint64, bool) {
	key, err := tlsConnectionGenerationKey(state)
	if err != nil {
		return 0, false
	}
	value, ok := qm.handshakeGen.LoadAndDelete(key)
	if !ok {
		return 0, false
	}
	generation, ok := value.(uint64)
	return generation, ok
}

func (qm *QUICManager) StartListener() error {
	qm.listenerMu.Lock()
	defer qm.listenerMu.Unlock()
	if qm.QUICListener != nil {
		return fmt.Errorf("QUIC listener already started")
	}
	serverTLS, _ := qm.serverTLSSnapshot()
	if serverTLS == nil {
		return fmt.Errorf("QUIC server TLS is not configured")
	}
	listenerTLS := serverTLS.Clone()
	listenerTLS.GetConfigForClient = qm.serverConfigForClient
	listenerTLS.NextProtos = []string{quicALPN}
	listener, err := qm.Transport.Listen(listenerTLS, defaultQUICConfig())
	if err != nil {
		return err
	}
	qm.QUICListener = listener

	go func() {
		for {
			conn, err := listener.Accept(qm.lifetime())
			if err != nil {
				return
			}
			go qm.handleIncomingConnection(conn)
		}
	}()

	return nil
}

// lifetime returns the manager context, which Close cancels. Managers built by
// tests without the constructor fall back to Background.
func (qm *QUICManager) lifetime() context.Context {
	if qm.ctx == nil {
		return context.Background()
	}
	return qm.ctx
}

// SetSession stores a QUIC connection in the sessions map.
// If a different conn already exists for peerID, it is closed after unlock.
func (qm *QUICManager) SetSession(peerID string, conn *quic.Conn) {
	if !qm.setSession(peerID, conn, nil) && conn != nil {
		_ = conn.CloseWithError(0, "manager closed")
	}
}

func (qm *QUICManager) setSession(peerID string, conn *quic.Conn, expectedGeneration *uint64) bool {
	qm.tlsMu.RLock()
	if expectedGeneration != nil && qm.tlsGeneration != *expectedGeneration {
		qm.tlsMu.RUnlock()
		return false
	}
	if qm.ctx != nil {
		select {
		case <-qm.ctx.Done():
			qm.tlsMu.RUnlock()
			return false
		default:
		}
	}

	qm.SessionsMu.Lock()
	if qm.Sessions == nil {
		qm.Sessions = make(map[string]*quic.Conn)
	}
	if _, blocked := qm.blockedPeers[peerID]; blocked {
		qm.SessionsMu.Unlock()
		qm.tlsMu.RUnlock()
		return false
	}
	old := qm.Sessions[peerID]
	qm.Sessions[peerID] = conn
	qm.SessionsMu.Unlock()
	qm.tlsMu.RUnlock()
	if old != nil && old != conn {
		_ = old.CloseWithError(0, "replaced")
	}
	return true
}

// CloseAndRemoveSession closes an existing session and deletes it from the map.
func (qm *QUICManager) CloseAndRemoveSession(peerID string, code quic.ApplicationErrorCode, msg string) {
	qm.SessionsMu.Lock()
	sess, exists := qm.Sessions[peerID]
	if exists {
		delete(qm.Sessions, peerID)
	}
	qm.SessionsMu.Unlock()
	if exists && sess != nil {
		_ = sess.CloseWithError(code, msg)
	}
}

// CloseSessionIf removes conn only when it is still the published session for
// peerID. A superseded route generation must never close a newer replacement.
func (qm *QUICManager) CloseSessionIf(peerID string, conn *quic.Conn, code quic.ApplicationErrorCode, msg string) {
	qm.SessionsMu.Lock()
	if qm.Sessions[peerID] == conn {
		delete(qm.Sessions, peerID)
	}
	qm.SessionsMu.Unlock()
	if conn != nil {
		_ = conn.CloseWithError(code, msg)
	}
}

// AllowPeerSessions permits authenticated QUIC sessions for a known route.
func (qm *QUICManager) AllowPeerSessions(peerID string) {
	qm.SessionsMu.Lock()
	delete(qm.blockedPeers, peerID)
	qm.SessionsMu.Unlock()
}

// BlockPeerSessions removes the current session and rejects late handshakes or
// dials until the router publishes a route for the peer again.
func (qm *QUICManager) BlockPeerSessions(peerID string, code quic.ApplicationErrorCode, msg string) {
	qm.SessionsMu.Lock()
	if qm.blockedPeers == nil {
		qm.blockedPeers = make(map[string]struct{})
	}
	qm.blockedPeers[peerID] = struct{}{}
	sess, exists := qm.Sessions[peerID]
	if exists {
		delete(qm.Sessions, peerID)
	}
	qm.SessionsMu.Unlock()
	if exists && sess != nil {
		_ = sess.CloseWithError(code, msg)
	}
}

func (qm *QUICManager) removeDial(peerID string) {
	qm.dialsMu.Lock()
	delete(qm.dials, peerID)
	qm.dialsMu.Unlock()
}

func (qm *QUICManager) detachSessions() []*quic.Conn {
	qm.SessionsMu.Lock()
	sessions := make([]*quic.Conn, 0, len(qm.Sessions))
	for id, sess := range qm.Sessions {
		sessions = append(sessions, sess)
		delete(qm.Sessions, id)
	}
	qm.SessionsMu.Unlock()
	return sessions
}

func closeQUICSessions(sessions []*quic.Conn, msg string) {
	for _, sess := range sessions {
		if sess != nil {
			_ = sess.CloseWithError(0, msg)
		}
	}
}

func (qm *QUICManager) Close() {
	qm.closeOnce.Do(func() {
		if qm.cancel != nil {
			qm.cancel()
		}
		qm.listenerMu.Lock()
		listener := qm.QUICListener
		qm.QUICListener = nil
		qm.listenerMu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		if qm.Transport != nil {
			_ = qm.Transport.Close()
		}
		qm.tlsMu.Lock()
		qm.tlsGeneration++
		qm.handshakeGen.Clear()
		sessions := qm.detachSessions()
		qm.tlsMu.Unlock()
		closeQUICSessions(sessions, "shutting down")
		if qm.PacketConn != nil {
			_ = qm.PacketConn.Close()
		}
	})
}

func (qm *QUICManager) handleIncomingConnection(conn *quic.Conn) {
	// Store session once authenticated/handshaked
	// PeerID is extracted from the client certificate CommonName
	state := conn.ConnectionState()
	// VerifyConnection recorded the generation only after this peer certificate
	// passed the trust policy selected for that generation.
	generation, current := qm.consumeHandshakeGeneration(&state.TLS)
	if !current {
		_ = conn.CloseWithError(0xff, "stale TLS generation")
		return
	}
	peerID, ok := PeerCNFromTLS(&state.TLS)
	if !ok {
		_ = conn.CloseWithError(0xff, "missing peer certificate")
		return
	}

	if !qm.setSession(peerID, conn, &generation) {
		_ = conn.CloseWithError(0, "stale TLS generation")
		return
	}

	if qm.Logger != nil {
		qm.Logger.Info("Direct QUIC connection accepted", "peer", peerID)
	}

	tlsState := state.TLS
	qm.serveIncomingStreams(conn, &tlsState)
}

func (qm *QUICManager) handleIncomingStream(stream *quic.Stream, tlsState *tls.ConnectionState) {
	defer func() { _ = stream.Close() }()

	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(stream, 10*1024*1024)))
	if err != nil {
		return
	}
	req.TLS = tlsState

	// Route to local http handler via httptest recorder
	w := &quicResponseWriter{stream: stream, header: make(http.Header)}
	qm.HTTPHandler.ServeHTTP(w, req)

	// Finalize response headers
	w.WriteHeader(http.StatusOK)
}

func (qm *QUICManager) GetSession(peerID string) (*quic.Conn, bool) {
	qm.SessionsMu.RLock()
	defer qm.SessionsMu.RUnlock()
	sess, exists := qm.Sessions[peerID]
	return sess, exists
}

// InitiateHolePunch triggers the peer exchange and ping probing
func (qm *QUICManager) InitiateHolePunch(ctx context.Context, peerID string, remoteAddresses []string, sendRelayReq func(string, string, []byte) ([]byte, error)) (*quic.Conn, error) {
	for {
		if conn, exists := qm.GetSession(peerID); exists {
			return conn, nil
		}

		qm.dialsMu.Lock()
		if qm.dials == nil {
			qm.dials = make(map[string]*dialResult)
		}
		res, exists := qm.dials[peerID]
		if !exists {
			res = &dialResult{done: make(chan struct{})}
			qm.dials[peerID] = res
			qm.dialsMu.Unlock()

			conn, err := qm.performHolePunch(ctx, peerID, remoteAddresses, sendRelayReq)
			res.conn = conn
			res.err = err
			qm.removeDial(peerID)
			close(res.done)
			return conn, err
		}
		qm.dialsMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-res.done:
			if ctx.Err() == nil && (errors.Is(res.err, context.Canceled) || errors.Is(res.err, context.DeadlineExceeded)) {
				continue
			}
			return res.conn, res.err
		}
	}
}

func (qm *QUICManager) performHolePunch(ctx context.Context, peerID string, remoteAddresses []string, sendRelayReq func(string, string, []byte) ([]byte, error)) (*quic.Conn, error) {
	candidateAddresses := append([]string(nil), remoteAddresses...)
	hasQUICAddress := false
	for _, rawAddress := range candidateAddresses {
		if _, ok := ParseQUICAddr(rawAddress); ok {
			hasQUICAddress = true
			break
		}
	}
	if !hasQUICAddress {
		return nil, fmt.Errorf("remote peer does not advertise quic:// public address")
	}

	localUDP := qm.PacketConn.LocalAddr().String()
	if qm.Logger != nil {
		qm.Logger.Info("Initiating UDP Hole Punching", "peer", peerID, "localUDP", localUDP)
	}

	// Prepare hole punch payload
	msg := HolePunchMessage{
		SenderID:  qm.LocalID,
		PublicUDP: qm.PublicUDPAddress(),
	}

	// Send to `/holepunch/init` of the remote peer via relay
	msgBytes, _ := json.Marshal(msg)
	respBytes, err := sendRelayReq(peerID, protocol.PathHolePunchInit, msgBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to send hole punch init over relay: %w", err)
	}

	var respMsg HolePunchMessage
	if err := json.Unmarshal(respBytes, &respMsg); err != nil {
		return nil, fmt.Errorf("failed to parse hole punch response: %w", err)
	}
	if respMsg.SenderID != peerID {
		return nil, fmt.Errorf("hole punch response sender mismatch: expected %q, got %q", peerID, respMsg.SenderID)
	}
	if respMsg.PublicUDP != "" {
		candidateAddresses = append([]string{FormatQUICAddr(respMsg.PublicUDP)}, candidateAddresses...)
	}

	candidates, err := qm.compatibleQUICAddresses(candidateAddresses)
	if err != nil {
		return nil, err
	}
	punchCtx, cancel := context.WithTimeout(ctx, protocol.HolePunchWait)
	defer cancel()

	attemptBudget := protocol.HolePunchWait / time.Duration(len(candidates))
	var attemptErrors []error
	for _, candidate := range candidates {
		attemptCtx, attemptCancel := context.WithTimeout(punchCtx, attemptBudget)
		err = qm.waitForHolePunch(attemptCtx, peerID, candidate, true)
		attemptCancel()
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", candidate, err))
			continue
		}
		conn, dialErr := qm.establishSessionAfterPunch(punchCtx, peerID, candidate)
		if dialErr != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", candidate, dialErr))
			continue
		}
		if qm.Logger != nil {
			qm.Logger.Info("UDP Hole Punching SUCCESS!", "peer", peerID, "remoteUDP", candidate)
		}
		return conn, nil
	}
	return nil, fmt.Errorf("all compatible QUIC addresses failed: %w", errors.Join(attemptErrors...))
}

func (qm *QUICManager) compatibleQUICAddresses(remoteAddresses []string) ([]*net.UDPAddr, error) {
	var (
		compatible []*net.UDPAddr
		failures   []error
		seen       = make(map[string]struct{})
	)
	for _, rawAddress := range remoteAddresses {
		remoteUDP, ok := ParseQUICAddr(rawAddress)
		if !ok {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", remoteUDP)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", rawAddress, err))
			continue
		}
		if err := requireMatchingUDPFamily(qm.PacketConn, addr); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", rawAddress, err))
			continue
		}
		key := addr.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		compatible = append(compatible, addr)
	}
	if len(compatible) == 0 {
		if len(failures) == 0 {
			return nil, fmt.Errorf("no quic:// addresses")
		}
		return nil, errors.Join(failures...)
	}
	return compatible, nil
}

// RespondToHolePunch runs the callee-side punch: ping burst, wait for peer, dial if localID < peerID.
// Shares dial coalescing with InitiateHolePunch so mutual punches do not double-dial.
func (qm *QUICManager) RespondToHolePunch(ctx context.Context, peerID, remoteUDP string) {
	if remoteUDP == "" {
		if qm.Logger != nil {
			qm.Logger.Warn("Hole punch respond skipped: empty remote UDP", "peer", peerID)
		}
		return
	}
	rUDPAddr, err := net.ResolveUDPAddr("udp", remoteUDP)
	if err != nil {
		if qm.Logger != nil {
			qm.Logger.Warn("Hole punch respond: resolve failed", "peer", peerID, "remoteUDP", remoteUDP, "error", err)
		}
		return
	}
	if err := requireMatchingUDPFamily(qm.PacketConn, rUDPAddr); err != nil {
		if qm.Logger != nil {
			qm.Logger.Warn("Hole punch respond: incompatible UDP endpoint", "peer", peerID, "remoteUDP", remoteUDP, "error", err)
		}
		return
	}

	if _, exists := qm.GetSession(peerID); exists {
		return
	}

	qm.dialsMu.Lock()
	if qm.dials == nil {
		qm.dials = make(map[string]*dialResult)
	}
	if res, exists := qm.dials[peerID]; exists {
		qm.dialsMu.Unlock()
		select {
		case <-ctx.Done():
		case <-res.done:
		}
		return
	}
	res := &dialResult{done: make(chan struct{})}
	qm.dials[peerID] = res
	qm.dialsMu.Unlock()

	defer func() {
		qm.removeDial(peerID)
		close(res.done)
	}()

	punchCtx, cancel := context.WithTimeout(ctx, protocol.HolePunchWait)
	defer cancel()

	// Arm demuxed waiter before bursting so early peer pings are not dropped.
	errCh := make(chan error, 1)
	go func() {
		errCh <- qm.waitForHolePunch(punchCtx, peerID, rUDPAddr, false)
	}()
	go BurstPings(qm.PacketConn, rUDPAddr, qm.LocalID, 20, 150*time.Millisecond)

	if err := <-errCh; err != nil {
		res.err = err
		if qm.Logger != nil {
			qm.Logger.Debug("Hole punch respond timed out", "peer", peerID, "error", err)
		}
		return
	}
	if qm.LocalID >= peerID {
		// Higher ID waits; initiator (lower) dials.
		return
	}
	conn, err := qm.establishSessionAfterPunch(punchCtx, peerID, rUDPAddr)
	res.conn = conn
	res.err = err
	if err != nil && qm.Logger != nil {
		qm.Logger.Warn("Failed to dial QUIC after hole punch respond", "peer", peerID, "error", err)
	}
}

// waitForHolePunch exchanges UDP pings until peerID is heard (or ctx done).
// When sendPings is true, also tick local pings (initiator path).
func (qm *QUICManager) waitForHolePunch(ctx context.Context, peerID string, rUDPAddr *net.UDPAddr, sendPings bool) error {
	pingPayload := HolePunchPingPayload(qm.LocalID)
	pingCh := qm.PacketConn.RegisterPingWait(peerID)
	defer qm.PacketConn.UnregisterPingWait(peerID)

	if sendPings {
		pingTicker := time.NewTicker(150 * time.Millisecond)
		defer pingTicker.Stop()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-pingTicker.C:
					_, _ = qm.PacketConn.WriteTo(pingPayload, rUDPAddr)
				}
			}
		}()
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("hole punching timeout to %s: %w", peerID, ctx.Err())
	case <-pingCh:
		BurstPings(qm.PacketConn, rUDPAddr, qm.LocalID, 3, 0)
		return nil
	}
}

// establishSessionAfterPunch dials when localID is lower; otherwise waits for an inbound session.
func (qm *QUICManager) establishSessionAfterPunch(ctx context.Context, peerID string, rUDPAddr *net.UDPAddr) (*quic.Conn, error) {
	if qm.LocalID < peerID {
		if qm.Logger != nil {
			qm.Logger.Debug("Dialing QUIC session as caller", "peer", peerID)
		}
		clientTLS, generation := qm.clientTLSSnapshot()
		if clientTLS == nil {
			return nil, fmt.Errorf("QUIC client TLS is not configured")
		}
		qconn, err := qm.Transport.Dial(ctx, rUDPAddr, clientTLS, defaultQUICConfig())
		if err != nil {
			return nil, fmt.Errorf("failed to dial direct QUIC session: %w", err)
		}

		tlsState := qconn.ConnectionState().TLS
		if err := VerifyTLSPeerCN(&tlsState, peerID); err != nil {
			_ = qconn.CloseWithError(0, "peer identity mismatch")
			return nil, fmt.Errorf("peer identity mismatch in QUIC: %w", err)
		}

		if !qm.setSession(peerID, qconn, &generation) {
			_ = qconn.CloseWithError(0, "tls rotated during dial")
			return nil, fmt.Errorf("QUIC TLS rotated during dial to %s", peerID)
		}

		if qm.Logger != nil {
			qm.Logger.Debug("Outbound QUIC session accept loop starting", "peer", peerID)
		}
		go qm.serveIncomingStreams(qconn, &tlsState)

		return qconn, nil
	}

	if qm.Logger != nil {
		qm.Logger.Debug("Waiting for incoming QUIC session as callee", "peer", peerID)
	}
	pollTimer := time.NewTicker(50 * time.Millisecond)
	defer pollTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for incoming QUIC session from %s: %w", peerID, ctx.Err())
		case <-pollTimer.C:
			sess, exists := qm.GetSession(peerID)
			if exists {
				return sess, nil
			}
		}
	}
}

// ReloadTLS refreshes QUIC TLS material from the live HTTP configs and drops sessions (L2).
func (qm *QUICManager) ReloadTLS(clientTLS, serverTLS *tls.Config) {
	cl := cloneQUICTLSConfig(clientTLS)
	srv := cloneQUICTLSConfig(serverTLS)

	qm.tlsMu.Lock()
	if cl != nil {
		qm.TLSClient = cl
	}
	if srv != nil {
		qm.TLSServer = srv
	}
	qm.tlsGeneration++
	qm.handshakeGen.Clear()
	sessions := qm.detachSessions()
	qm.tlsMu.Unlock()

	closeQUICSessions(sessions, "tls rotated")
}

type quicResponseWriter struct {
	stream        *quic.Stream
	header        http.Header
	headerWritten bool
}

func (w *quicResponseWriter) Header() http.Header {
	return w.header
}

func (w *quicResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	return w.stream.Write(b)
}

func (w *quicResponseWriter) WriteHeader(statusCode int) {
	if w.headerWritten {
		return
	}
	w.headerWritten = true

	// Write simple HTTP response status line & headers directly to QUIC stream
	statusText := http.StatusText(statusCode)
	if statusText == "" {
		statusText = "OK"
	}
	_, _ = fmt.Fprintf(w.stream, "HTTP/1.1 %d %s\r\n", statusCode, statusText)
	for k, v := range w.header {
		for _, val := range v {
			_, _ = fmt.Fprintf(w.stream, "%s: %s\r\n", k, val)
		}
	}
	_, _ = fmt.Fprint(w.stream, "\r\n")
}
