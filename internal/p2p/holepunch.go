package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go qm.handleIncomingStream(stream, tlsState)
	}
}

// HolePunchPacketConn wraps net.PacketConn to intercept hole punching pings
type HolePunchPacketConn struct {
	net.PacketConn
	PingCh chan string // receives sender IDs of successful pings
}

func NewHolePunchPacketConn(pc net.PacketConn) *HolePunchPacketConn {
	return &HolePunchPacketConn{
		PacketConn: pc,
		PingCh:     make(chan string, 100),
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
			select {
			case h.PingCh <- senderID:
			default:
			}
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

// QUICManager manages active direct QUIC sessions and incoming listeners
type QUICManager struct {
	LocalID       string
	PublicUDPAddr string
	PacketConn    *HolePunchPacketConn
	QUICListener  *quic.Listener
	Transport     *quic.Transport
	Sessions      map[string]*quic.Conn
	SessionsMu    sync.RWMutex
	TLSClient     *tls.Config
	TLSServer     *tls.Config
	HTTPHandler   http.Handler
	Logger        interface {
		Info(msg string, args ...any)
		Debug(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
	}
	dialsMu sync.Mutex
	dials   map[string]*dialResult
}

func NewQUICManager(localID string, conn *net.UDPConn, clientTLS, serverTLS *tls.Config, handler http.Handler, logger any) *QUICManager {
	wrapped := NewHolePunchPacketConn(conn)

	// Clone TLS configs and append NextProtos required by QUIC
	clTls := clientTLS.Clone()
	clTls.NextProtos = []string{"proxyma-p2p"}
	srvTls := serverTLS.Clone()
	srvTls.NextProtos = []string{"proxyma-p2p"}

	transport := &quic.Transport{
		Conn: wrapped,
	}

	qm := &QUICManager{
		LocalID:     localID,
		PacketConn:  wrapped,
		Transport:   transport,
		Sessions:    make(map[string]*quic.Conn),
		TLSClient:   clTls,
		TLSServer:   srvTls,
		HTTPHandler: handler,
		dials:       make(map[string]*dialResult),
	}

	// Cast logging interface
	if casted, ok := logger.(interface {
		Info(msg string, args ...any)
		Debug(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
	}); ok {
		qm.Logger = casted
	}

	return qm
}

func (qm *QUICManager) StartListener() error {
	listener, err := qm.Transport.Listen(qm.TLSServer, defaultQUICConfig())
	if err != nil {
		return err
	}
	qm.QUICListener = listener

	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go qm.handleIncomingConnection(conn)
		}
	}()

	return nil
}

// SetSession stores a QUIC connection in the sessions map.
func (qm *QUICManager) SetSession(peerID string, conn *quic.Conn) {
	qm.SessionsMu.Lock()
	qm.Sessions[peerID] = conn
	qm.SessionsMu.Unlock()
}

// CloseAndRemoveSession closes an existing session and deletes it from the map.
func (qm *QUICManager) CloseAndRemoveSession(peerID string, code quic.ApplicationErrorCode, msg string) {
	qm.SessionsMu.Lock()
	sess, exists := qm.Sessions[peerID]
	if exists {
		delete(qm.Sessions, peerID)
	}
	qm.SessionsMu.Unlock()
	if exists {
		_ = sess.CloseWithError(code, msg)
	}
}

func (qm *QUICManager) removeDial(peerID string) {
	qm.dialsMu.Lock()
	delete(qm.dials, peerID)
	qm.dialsMu.Unlock()
}

func (qm *QUICManager) Close() {
	if qm.QUICListener != nil {
		_ = qm.QUICListener.Close()
	}
	if qm.Transport != nil {
		_ = qm.Transport.Close()
	}
	qm.SessionsMu.Lock()
	for _, sess := range qm.Sessions {
		_ = sess.CloseWithError(0, "shutting down")
	}
	qm.SessionsMu.Unlock()
}

func (qm *QUICManager) handleIncomingConnection(conn *quic.Conn) {
	// Store session once authenticated/handshaked
	// PeerID is extracted from the client certificate CommonName
	state := conn.ConnectionState()
	peerID, ok := PeerCNFromTLS(&state.TLS)
	if !ok {
		_ = conn.CloseWithError(0xff, "missing peer certificate")
		return
	}

	qm.SetSession(peerID, conn)

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
	if conn, exists := qm.GetSession(peerID); exists {
		return conn, nil
	}

	qm.dialsMu.Lock()
	if qm.dials == nil {
		qm.dials = make(map[string]*dialResult)
	}
	res, exists := qm.dials[peerID]
	if exists {
		qm.dialsMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-res.done:
			return res.conn, res.err
		}
	}

	res = &dialResult{done: make(chan struct{})}
	qm.dials[peerID] = res
	qm.dialsMu.Unlock()

	defer func() {
		qm.removeDial(peerID)
		close(res.done)
	}()

	conn, err := qm.performHolePunch(ctx, peerID, remoteAddresses, sendRelayReq)
	res.conn = conn
	res.err = err
	return conn, err
}

func (qm *QUICManager) performHolePunch(ctx context.Context, peerID string, remoteAddresses []string, sendRelayReq func(string, string, []byte) ([]byte, error)) (*quic.Conn, error) {
	// Find the remote public UDP address in remoteAddresses list
	var remoteUDP string
	if quicAddr, ok := FirstQUICAddr(remoteAddresses); ok {
		remoteUDP, _ = ParseQUICAddr(quicAddr)
	}
	if remoteUDP == "" {
		return nil, fmt.Errorf("remote peer does not advertise quic:// public address")
	}

	localUDP := qm.PacketConn.LocalAddr().String()
	if qm.Logger != nil {
		qm.Logger.Info("Initiating UDP Hole Punching", "peer", peerID, "remoteUDP", remoteUDP, "localUDP", localUDP)
	}

	// Prepare hole punch payload
	msg := HolePunchMessage{
		SenderID:  qm.LocalID,
		PublicUDP: qm.PublicUDPAddr,
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

	// Start pinging in background
	rUDPAddr, err := net.ResolveUDPAddr("udp", remoteUDP)
	if err != nil {
		return nil, err
	}

	pingPayload := HolePunchPingPayload(qm.LocalID)

	// Send pings and wait for a ping from them
	timeout := time.After(8 * time.Second)
	pingTicker := time.NewTicker(150 * time.Millisecond)
	defer pingTicker.Stop()

	successCh := make(chan bool, 1)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout:
				return
			case <-pingTicker.C:
				_, _ = qm.PacketConn.WriteTo(pingPayload, rUDPAddr)
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout:
				return
			case sender := <-qm.PacketConn.PingCh:
				if sender == peerID {
					BurstPings(qm.PacketConn, rUDPAddr, qm.LocalID, 3, 0)
					successCh <- true
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeout:
		return nil, fmt.Errorf("hole punching timeout to %s", peerID)
	case <-successCh:
		if qm.Logger != nil {
			qm.Logger.Info("UDP Hole Punching SUCCESS!", "peer", peerID)
		}
	}

	// Establish QUIC session
	// Dialer is the one with the lower lexicographical ID
	if qm.LocalID < peerID {
		if qm.Logger != nil {
			qm.Logger.Debug("Dialing QUIC session as caller", "peer", peerID)
		}
		qconn, err := qm.Transport.Dial(ctx, rUDPAddr, qm.TLSClient, defaultQUICConfig())
		if err != nil {
			return nil, fmt.Errorf("failed to dial direct QUIC session: %w", err)
		}

		tlsState := qconn.ConnectionState().TLS
		if err := VerifyTLSPeerCN(&tlsState, peerID); err != nil {
			_ = qconn.CloseWithError(0, "peer identity mismatch")
			return nil, fmt.Errorf("peer identity mismatch in QUIC: %w", err)
		}

		qm.SetSession(peerID, qconn)

		if qm.Logger != nil {
			qm.Logger.Debug("Outbound QUIC session accept loop starting", "peer", peerID)
		}
		go qm.serveIncomingStreams(qconn, &tlsState)

		return qconn, nil
	} else {
		if qm.Logger != nil {
			qm.Logger.Debug("Waiting for incoming QUIC session as callee", "peer", peerID)
		}
		// Wait for the accepted connection to appear in our Sessions map
		pollTimer := time.NewTicker(50 * time.Millisecond)
		defer pollTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timeout:
				return nil, fmt.Errorf("timeout waiting for incoming QUIC session from %s", peerID)
			case <-pollTimer.C:
				sess, exists := qm.GetSession(peerID)
				if exists {
					return sess, nil
				}
			}
		}
	}
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
