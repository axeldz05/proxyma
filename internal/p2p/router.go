package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type BypassHolePunchKey struct{}

// peerHandshakeRoundTripper is implemented by transports that can bind the
// expected peer identity to their TLS handshake. It also gives focused tests a
// safe seam without weakening production transports.
type peerHandshakeRoundTripper interface {
	RoundTripPeerVerified(*http.Request, string) (*http.Response, error)
}

type prewarmAttempt struct {
	cancel     context.CancelFunc
	qm         *QUICManager
	generation uint64
}

// P2PRoundTripper intercepts http://*.proxyma.local requests and attempts to route them
// either directly to the peer's known addresses or via a fallback Relay sponsor.
type P2PRoundTripper struct {
	mu              sync.RWMutex
	routes          map[string]protocol.AddressRecord
	SponsorAddress  string
	Base            http.RoundTripper
	Logger          *slog.Logger
	NodeID          string
	OwnAddress      string
	QM              *QUICManager
	prewarms        map[string]*prewarmAttempt
	routeGeneration map[string]uint64
	lifetime        context.Context
	cancelLifetime  context.CancelFunc
	prewarmWG       sync.WaitGroup
	closed          bool
	closeDone       chan struct{}

	// ProbeDialContext overrides the fail-fast TCP probe dialer. Nil uses
	// net.Dialer.DialContext; tests may inject a deterministic blocking probe.
	ProbeDialContext func(context.Context, string, string) (net.Conn, error)
}

func (r *P2PRoundTripper) UpdatePeerRoute(peerID string, record protocol.AddressRecord) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.ensureLifetimeLocked()
	if r.routes == nil {
		r.routes = make(map[string]protocol.AddressRecord)
	}
	if current := r.prewarms[peerID]; current != nil {
		current.cancel()
		delete(r.prewarms, peerID)
	}
	if r.routeGeneration == nil {
		r.routeGeneration = make(map[string]uint64)
	}
	r.routeGeneration[peerID]++
	generation := r.routeGeneration[peerID]
	r.routes[peerID] = record
	qm := r.QM
	sponsorAddr := r.SponsorAddress
	var attempt *prewarmAttempt
	var prewarmCtx context.Context
	if qm != nil {
		qm.AllowPeerSessions(peerID)
		if sponsorAddr != "" {
			if _, hasQUIC := FirstQUICAddr(record.Addresses); hasQUIC {
				if _, exists := qm.GetSession(peerID); !exists {
					prewarmCtx, attempt = r.newPrewarmAttemptLocked(peerID, qm, generation)
					r.prewarmWG.Add(1)
				}
			}
		}
	}
	r.mu.Unlock()

	if attempt != nil {
		go r.prewarmConnection(prewarmCtx, attempt, peerID, record, sponsorAddr)
	}
}

func (r *P2PRoundTripper) ensureLifetimeLocked() {
	if r.closeDone == nil {
		r.closeDone = make(chan struct{})
	}
	if r.lifetime == nil {
		r.lifetime, r.cancelLifetime = context.WithCancel(context.Background())
	}
}

func (r *P2PRoundTripper) newPrewarmAttemptLocked(peerID string, qm *QUICManager, generation uint64) (context.Context, *prewarmAttempt) {
	if r.prewarms == nil {
		r.prewarms = make(map[string]*prewarmAttempt)
	}
	ctx, cancel := context.WithTimeout(r.lifetime, protocol.PrewarmHolePunch)
	attempt := &prewarmAttempt{cancel: cancel, qm: qm, generation: generation}
	r.prewarms[peerID] = attempt
	return ctx, attempt
}

func (r *P2PRoundTripper) prewarmConnection(ctx context.Context, attempt *prewarmAttempt, peerID string, record protocol.AddressRecord, sponsorAddr string) {
	defer r.prewarmWG.Done()
	defer attempt.cancel()
	r.logDebug("Pre-warming direct QUIC connection to peer", "peerID", peerID)
	session, err := attempt.qm.InitiateHolePunch(ctx, peerID, record.Addresses, func(targetPeer, action string, payload []byte) ([]byte, error) {
		return r.sendRelayMessage(ctx, sponsorAddr, targetPeer, action, payload)
	})

	r.mu.Lock()
	if r.prewarms[peerID] == attempt {
		delete(r.prewarms, peerID)
	}
	_, routeExists := r.routes[peerID]
	currentGeneration := r.routeGeneration[peerID]
	if !routeExists {
		// InitiateHolePunch may have completed concurrently with cancellation.
		// Keep the peer blocked so late inbound and outbound handshakes cannot
		// repopulate Sessions after route removal.
		attempt.qm.BlockPeerSessions(peerID, 0, "peer removed during prewarm")
	} else if currentGeneration != attempt.generation && session != nil {
		attempt.qm.CloseSessionIf(peerID, session, 0, "superseded route generation")
	}
	r.mu.Unlock()

	if err != nil {
		r.logDebug("Pre-warming direct QUIC connection failed", "peerID", peerID, "error", err)
		return
	}
	if !routeExists || currentGeneration != attempt.generation {
		r.logDebug("Discarding pre-warmed connection for removed peer", "peerID", peerID)
		return
	}
	r.logDebug("Pre-warming direct QUIC connection succeeded!", "peerID", peerID)
}

func (r *P2PRoundTripper) sendRelayMessage(ctx context.Context, sponsorAddr string, targetPeer, path string, body []byte) ([]byte, error) {
	relayReq := NewRelayRequest(targetPeer, http.MethodPost, path, body, nil)
	relayRes, err := ForwardRelay(ctx, r.Base, sponsorAddr, relayReq)
	if err != nil {
		return nil, err
	}
	if relayRes.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer error response: %d", relayRes.StatusCode)
	}
	return relayRes.Body, nil
}

func (r *P2PRoundTripper) RemovePeerRoute(peerID string) {
	r.mu.Lock()
	attempt := r.prewarms[peerID]
	if attempt != nil {
		attempt.cancel()
		delete(r.prewarms, peerID)
	}
	if r.routes != nil {
		delete(r.routes, peerID)
	}
	if r.routeGeneration == nil {
		r.routeGeneration = make(map[string]uint64)
	}
	r.routeGeneration[peerID]++
	qm := r.QM
	if qm != nil {
		qm.BlockPeerSessions(peerID, 0, "peer removed")
	}
	if attempt != nil && attempt.qm != qm {
		attempt.qm.BlockPeerSessions(peerID, 0, "peer removed")
	}
	r.mu.Unlock()
}

func (r *P2PRoundTripper) UpdateSponsorAddress(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SponsorAddress = addr
}

func (r *P2PRoundTripper) logDebug(msg string, args ...any) {
	if r.Logger != nil {
		r.Logger.Debug(msg, args...)
	}
}

func (r *P2PRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.logDebug("P2P router called", "host", req.URL.Host)
	peerID, ok := protocol.ParsePeerLocalHost(req.URL.Hostname())
	if !ok {
		r.logDebug("Skipping routing: host does not match proxyma.local suffix", "host", req.URL.Host)
		return r.Base.RoundTrip(req)
	}
	r.logDebug("Host matches proxyma.local suffix, resolving peer", "host", req.URL.Host)

	r.mu.RLock()
	record, exists := r.routes[peerID]
	sponsorAddr := r.SponsorAddress
	qm := r.QM
	nodeID := r.NodeID
	ownAddress := r.OwnAddress
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown peer ID in routing: %s", peerID)
	}

	// Clone the request so we don't modify the original. Buffer the body once so
	// relay / direct / QUIC fallbacks can each re-read it after a failed phase.
	clone := req.Clone(req.Context())
	if err := bufferRequestBody(clone); err != nil {
		return nil, fmt.Errorf("buffer request body: %w", err)
	}

	// Try existing QUIC session first
	if resp, handled := r.tryExistingSession(qm, clone, req, peerID); handled {
		return resp, nil
	}

	var lastErr error
	keepErr := func(err error) {
		if err != nil && lastErr == nil {
			lastErr = err
		}
	}

	// Hostnames first (docker DNS); IPs after relay so stale bridge/STUN
	// addresses cannot burn the whole parent RPC deadline.
	hostnames, ipAddrs := splitDirectAddresses(record.Addresses)

	// Phase 1a: DNS hostnames only (fast fail when unknown)
	resp, err := r.tryDirectAddresses(clone, peerID, hostnames, nodeID, ownAddress)
	if resp != nil {
		return resp, nil
	}
	lastErr = err

	// Phase 1.5: UDP Hole Punching to establish QUIC session
	if resp, err, handled := r.tryHolePunchAndRoute(qm, clone, req, peerID, record, sponsorAddr); handled {
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}

	// Phase 2: Relay Fallback before IP literals (critical for partitioned nets)
	if sponsorAddr != "" {
		relayRes, relayErr := r.tryRelay(clone, peerID, sponsorAddr)
		switch {
		case relayErr == nil:
			return relayRes.ToHTTPResponse(req), nil
		case errors.Is(relayErr, errRelayPayloadTooLarge):
			return nil, relayErr
		default:
			keepErr(relayErr)
		}
	}

	// Phase 3: IP literals last (private docker IPs, then public STUN)
	resp, err = r.tryDirectAddresses(clone, peerID, ipAddrs, nodeID, ownAddress)
	if resp != nil {
		return resp, nil
	}
	if err != nil {
		lastErr = err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to route to peer %s: %w", peerID, lastErr)
	}
	return nil, fmt.Errorf("no addresses available for peer %s", peerID)
}

// tryDirectAddresses dials addrs in order and returns the first response from a
// peer whose certificate matches peerID. When none works it returns the last
// failure, which may be nil if every address was skipped.
func (r *P2PRoundTripper) tryDirectAddresses(clone *http.Request, peerID string, addrs []string, nodeID, ownAddress string) (*http.Response, error) {
	var lastErr error
	for _, rawAddr := range addrs {
		parsedAddr, err := url.Parse(rawAddr)
		if err != nil {
			lastErr = err
			continue
		}
		if parsedAddr.Scheme == "quic" || r.isOwnAddress(parsedAddr, peerID, nodeID, ownAddress) {
			continue
		}

		if err := resetRequestBody(clone); err != nil {
			return nil, err
		}
		clone.URL.Scheme = parsedAddr.Scheme
		clone.URL.Host = parsedAddr.Host
		r.logDebug("Routing direct request", "url", clone.URL.String())

		resp, err := r.dialDirect(clone, peerID, parsedAddr)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// isOwnAddress reports whether parsedAddr points back at this node, which would
// make the request loop. Reaching ourselves by ID is legitimate.
func (r *P2PRoundTripper) isOwnAddress(parsedAddr *url.URL, peerID, nodeID, ownAddress string) bool {
	if ownAddress == "" || peerID == nodeID {
		return false
	}
	ownParsed, err := url.Parse(ownAddress)
	if err != nil {
		return false
	}
	host := parsedAddr.Hostname()
	port := defaultPortForURL(parsedAddr)
	sameLoopbackPort := utils.IsLoopbackHost(host) && port == defaultPortForURL(ownParsed)
	if !sameLoopbackPort && parsedAddr.Host != ownParsed.Host {
		return false
	}
	r.logDebug("Skipping address that matches our own node host/port", "peerID", peerID, "host", host, "port", port)
	return true
}

// dialDirect probes the TCP port before issuing the request, then verifies that
// the TLS identity really belongs to peerID.
func (r *P2PRoundTripper) dialDirect(clone *http.Request, peerID string, parsedAddr *url.URL) (*http.Response, error) {
	if parsedAddr.Scheme != "https" {
		return nil, fmt.Errorf("direct P2P route requires HTTPS: %s", parsedAddr.String())
	}

	// TCP probe fail-fast on dead IPs without binding the HTTP body to a
	// short request context (that aborted large blob downloads).
	probeAddr := net.JoinHostPort(parsedAddr.Hostname(), defaultPortForURL(parsedAddr))
	probeCtx, probeCancel := context.WithTimeout(clone.Context(), protocol.DialTimeoutRouteProbe)
	defer probeCancel()
	dialContext := r.ProbeDialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{}).DialContext
	}
	conn, dialErr := dialContext(probeCtx, "tcp", probeAddr)
	if dialErr != nil {
		return nil, dialErr
	}
	_ = conn.Close()

	resp, err := roundTripPeerVerified(r.Base, clone.Clone(clone.Context()), peerID)
	if err != nil {
		return nil, err
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("direct HTTPS route returned no peer certificate")
	}
	cert := resp.TLS.PeerCertificates[0]
	if vErr := VerifyPeerCN(cert, peerID); vErr != nil {
		_ = resp.Body.Close()
		r.logDebug("Rejecting direct connection: peer identity mismatch", "expected", peerID, "got", cert.Subject.CommonName)
		return nil, vErr
	}
	return resp, nil
}

func roundTripPeerVerified(base http.RoundTripper, req *http.Request, peerID string) (*http.Response, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	if verifier, ok := base.(peerHandshakeRoundTripper); ok {
		return verifier.RoundTripPeerVerified(req, peerID)
	}
	verified, err := peerVerifiedTransport(base, peerID)
	if err != nil {
		return nil, err
	}
	defer closeIdle(verified)
	return verified.RoundTrip(req)
}

func peerVerifiedTransport(base http.RoundTripper, peerID string) (http.RoundTripper, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	switch transport := base.(type) {
	case *BandwidthRoundTripper:
		wrapped := *transport
		verifiedBase, err := peerVerifiedTransport(transport.Base, peerID)
		if err != nil {
			return nil, err
		}
		wrapped.Base = verifiedBase
		return &wrapped, nil
	case *http.Transport:
		clone := transport.Clone()
		tlsConfig := clone.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		verifyConnection := tlsConfig.VerifyConnection
		tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if verifyConnection != nil {
				if err := verifyConnection(state); err != nil {
					return err
				}
			}
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("peer identity mismatch: missing certificate")
			}
			return VerifyPeerCN(state.PeerCertificates[0], peerID)
		}
		clone.TLSClientConfig = tlsConfig
		return clone, nil
	default:
		return nil, fmt.Errorf("wrapped direct HTTPS transport %T cannot enforce peer identity during TLS handshake", base)
	}
}

// errRelayPayloadTooLarge aborts routing outright: no other phase can carry a
// body the relay refuses.
var errRelayPayloadTooLarge = errors.New("payload exceeds relay size limit")

// bufferRequestBody materializes clone.Body so later routing phases can replay it.
func bufferRequestBody(clone *http.Request) error {
	if clone.Body == nil || clone.Body == http.NoBody {
		return nil
	}
	if clone.GetBody != nil {
		return nil
	}
	bodyBytes, err := io.ReadAll(clone.Body)
	_ = clone.Body.Close()
	if err != nil {
		return err
	}
	clone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	clone.ContentLength = int64(len(bodyBytes))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}
	return nil
}

func resetRequestBody(clone *http.Request) error {
	if clone.GetBody == nil {
		return nil
	}
	body, err := clone.GetBody()
	if err != nil {
		return err
	}
	if clone.Body != nil {
		_ = clone.Body.Close()
	}
	clone.Body = body
	return nil
}

// tryRelay tunnels the request through the sponsor.
func (r *P2PRoundTripper) tryRelay(clone *http.Request, peerID, sponsorAddr string) (protocol.RelayResponse, error) {
	if err := resetRequestBody(clone); err != nil {
		return protocol.RelayResponse{}, fmt.Errorf("reset relay body: %w", err)
	}
	relayReq := NewRelayRequest(peerID, clone.Method, RequestPathWithQuery(clone.URL), nil, nil)
	if clone.Body != nil {
		bodyBytes, err := io.ReadAll(clone.Body)
		_ = clone.Body.Close()
		if err != nil {
			return protocol.RelayResponse{}, fmt.Errorf("read relay body: %w", err)
		}
		if len(bodyBytes) > protocol.MaxRelayBodyBytes {
			return protocol.RelayResponse{}, fmt.Errorf("%w of %dKB for relay fallback",
				errRelayPayloadTooLarge, protocol.MaxRelayBodyBytes/1024)
		}
		relayReq.Body = bodyBytes
	}
	relayReq.Headers = FlattenHTTPHeader(clone.Header)
	return ForwardRelay(clone.Context(), r.Base, sponsorAddr, relayReq)
}

func (r *P2PRoundTripper) tryExistingSession(qm *QUICManager, clone *http.Request, req *http.Request, peerID string) (*http.Response, bool) {
	if qm == nil {
		return nil, false
	}
	sess, exists := qm.GetSession(peerID)
	if !exists {
		return nil, false
	}

	r.logDebug("Routing request directly over existing QUIC session", "peerID", peerID)
	return r.routeOverQUICSession(qm, peerID, sess, req, clone)
}

func (r *P2PRoundTripper) routeOverQUICSession(qm *QUICManager, peerID string, sess *quic.Conn, req, clone *http.Request) (*http.Response, bool) {
	resp, err := r.sendRequestOverQUIC(sess, req, clone)
	if err != nil {
		r.logDebug("Failed to send request over QUIC session", "peerID", peerID, "error", err)
		qm.CloseAndRemoveSession(peerID, 0x01, "transport error")
		return nil, false
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		qm.CloseAndRemoveSession(peerID, 0x02, "unauthorized")
	}
	return resp, true
}

func (r *P2PRoundTripper) tryHolePunchAndRoute(qm *QUICManager, clone *http.Request, req *http.Request, peerID string, record protocol.AddressRecord, sponsorAddr string) (*http.Response, error, bool) {
	if qm == nil || sponsorAddr == "" {
		return nil, nil, false
	}
	bypass, _ := clone.Context().Value(BypassHolePunchKey{}).(bool)
	if bypass {
		return nil, nil, false
	}
	hasQuicAddress := false
	if _, ok := FirstQUICAddr(record.Addresses); ok {
		hasQuicAddress = true
	}
	if !hasQuicAddress {
		return nil, nil, false
	}

	r.logDebug("Attempting UDP Hole Punching via Sponsor", "peerID", peerID)
	sendRelayReq := func(targetPeer, path string, body []byte) ([]byte, error) {
		return r.sendRelayMessage(clone.Context(), sponsorAddr, targetPeer, path, body)
	}

	holePunchTimeout := protocol.HolePunchAttempt
	if deadline, ok := clone.Context().Deadline(); ok {
		timeLeft := time.Until(deadline)
		if timeLeft > 1000*time.Millisecond {
			holePunchTimeout = min(timeLeft-1000*time.Millisecond, protocol.HolePunchAttempt)
		} else {
			holePunchTimeout = 0
		}
	}
	if holePunchTimeout <= 0 {
		return nil, nil, false
	}

	ctx, cancel := context.WithTimeout(clone.Context(), holePunchTimeout)
	sess, err := qm.InitiateHolePunch(ctx, peerID, record.Addresses, sendRelayReq)
	cancel()
	if err != nil {
		r.logDebug("UDP Hole Punching failed", "peerID", peerID, "error", err)
		return nil, err, true
	}

	r.logDebug("UDP Hole Punching succeeded, routing over direct QUIC", "peerID", peerID)
	resp, ok := r.routeOverQUICSession(qm, peerID, sess, req, clone)
	if !ok {
		return nil, fmt.Errorf("failed to send request over newly punched QUIC session"), true
	}
	return resp, nil, true
}

func (r *P2PRoundTripper) sendRequestOverQUIC(sess *quic.Conn, req *http.Request, clone *http.Request) (*http.Response, error) {
	if err := resetRequestBody(clone); err != nil {
		return nil, err
	}
	stream, err := sess.OpenStreamSync(req.Context())
	if err != nil {
		return nil, err
	}

	// Adjust headers and host for remote local HTTP routing
	clone.URL.Scheme = "http"
	clone.URL.Host = "127.0.0.1"

	err = clone.Write(stream)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}

	resp.Body = &StreamCloseWrapper{ReadCloser: resp.Body, stream: stream}
	return resp, nil
}

type StreamCloseWrapper struct {
	io.ReadCloser
	stream *quic.Stream
}

func (s *StreamCloseWrapper) Close() error {
	err1 := s.ReadCloser.Close()
	err2 := s.stream.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func generateSecureReqID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano()) // Fallback
	}
	return hex.EncodeToString(bytes)
}

func defaultPortForURL(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// splitDirectAddresses separates DNS hostnames from IP literals.
// IPs (private or public) are tried after relay so stale docker/STUN
// endpoints cannot exhaust the parent RPC deadline before fallback.
func splitDirectAddresses(addrs []string) (hostnames, ipAddrs []string) {
	for _, raw := range addrs {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "quic" {
			continue
		}
		host := parsed.Hostname()
		if host == "" {
			continue
		}
		if net.ParseIP(host) == nil {
			hostnames = append(hostnames, raw)
			continue
		}
		ipAddrs = append(ipAddrs, raw)
	}
	return hostnames, ipAddrs
}

func (r *P2PRoundTripper) CloseIdleConnections() {
	closeIdle(r.Base)
}

func (r *P2PRoundTripper) SetNodeID(id string) {
	r.mu.Lock()
	r.NodeID = id
	r.mu.Unlock()
}

func (r *P2PRoundTripper) SetOwnAddress(addr string) {
	r.mu.Lock()
	r.OwnAddress = addr
	r.mu.Unlock()
}

func (r *P2PRoundTripper) SetQUICManager(qm *QUICManager) {
	r.mu.Lock()
	if !r.closed {
		if r.QM != qm {
			if r.routeGeneration == nil {
				r.routeGeneration = make(map[string]uint64)
			}
			for peerID, attempt := range r.prewarms {
				attempt.cancel()
				delete(r.prewarms, peerID)
				r.routeGeneration[peerID]++
			}
		}
		r.QM = qm
	}
	r.mu.Unlock()
}

func (r *P2PRoundTripper) SetLifetimeContext(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.ensureLifetimeLocked()
	if r.cancelLifetime != nil {
		r.cancelLifetime()
	}
	if r.routeGeneration == nil {
		r.routeGeneration = make(map[string]uint64)
	}
	for peerID, attempt := range r.prewarms {
		attempt.cancel()
		delete(r.prewarms, peerID)
		r.routeGeneration[peerID]++
	}
	r.lifetime, r.cancelLifetime = context.WithCancel(parent)
}

func (r *P2PRoundTripper) Close() {
	r.mu.Lock()
	r.ensureLifetimeLocked()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		return
	}
	r.closed = true
	if r.cancelLifetime != nil {
		r.cancelLifetime()
	}
	for peerID, attempt := range r.prewarms {
		attempt.cancel()
		delete(r.prewarms, peerID)
	}
	done := r.closeDone
	r.mu.Unlock()

	r.prewarmWG.Wait()
	closeIdle(r.Base)
	close(done)
}
