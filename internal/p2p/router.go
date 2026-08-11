package p2p

import (
	"bufio"
	"context"
	"crypto/rand"
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

// P2PRoundTripper intercepts http://*.proxyma.local requests and attempts to route them
// either directly to the peer's known addresses or via a fallback Relay sponsor.
type P2PRoundTripper struct {
	mu             sync.RWMutex
	routes         map[string]protocol.AddressRecord
	SponsorAddress string
	Base           http.RoundTripper
	Logger         *slog.Logger
	NodeID         string
	OwnAddress     string
	QM             *QUICManager
}

func (r *P2PRoundTripper) UpdatePeerRoute(peerID string, record protocol.AddressRecord) {
	r.mu.Lock()
	if r.routes == nil {
		r.routes = make(map[string]protocol.AddressRecord)
	}
	r.routes[peerID] = record
	r.mu.Unlock()

	if r.QM != nil {
		if _, exists := r.QM.GetSession(peerID); !exists {
			go r.prewarmConnection(peerID, record)
		}
	}
}

func (r *P2PRoundTripper) prewarmConnection(peerID string, record protocol.AddressRecord) {
	if _, ok := FirstQUICAddr(record.Addresses); !ok {
		return
	}

	r.mu.RLock()
	sponsorAddr := r.SponsorAddress
	r.mu.RUnlock()
	if sponsorAddr == "" {
		return
	}

	r.logDebug("Pre-warming direct QUIC connection to peer", "peerID", peerID)
	ctx, cancel := context.WithTimeout(context.Background(), protocol.PrewarmHolePunch)
	defer cancel()

	_, err := r.QM.InitiateHolePunch(ctx, peerID, record.Addresses, func(targetPeer, action string, payload []byte) ([]byte, error) {
		return r.sendRelayMessage(ctx, sponsorAddr, targetPeer, action, payload)
	})
	if err != nil {
		r.logDebug("Pre-warming direct QUIC connection failed", "peerID", peerID, "error", err)
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
	defer r.mu.Unlock()
	if r.routes != nil {
		delete(r.routes, peerID)
	}
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
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown peer ID in routing: %s", peerID)
	}

	// Clone the request so we don't modify the original
	clone := req.Clone(req.Context())

	// Try existing QUIC session first
	if resp, handled := r.tryExistingSession(clone, req, peerID); handled {
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
	resp, err := r.tryDirectAddresses(clone, peerID, hostnames)
	if resp != nil {
		return resp, nil
	}
	lastErr = err

	// Phase 1.5: UDP Hole Punching to establish QUIC session
	if resp, err, handled := r.tryHolePunchAndRoute(clone, req, peerID, record, sponsorAddr); handled {
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
	resp, err = r.tryDirectAddresses(clone, peerID, ipAddrs)
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
func (r *P2PRoundTripper) tryDirectAddresses(clone *http.Request, peerID string, addrs []string) (*http.Response, error) {
	var lastErr error
	for _, rawAddr := range addrs {
		parsedAddr, err := url.Parse(rawAddr)
		if err != nil {
			lastErr = err
			continue
		}
		if parsedAddr.Scheme == "quic" || r.isOwnAddress(parsedAddr, peerID) {
			continue
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
func (r *P2PRoundTripper) isOwnAddress(parsedAddr *url.URL, peerID string) bool {
	if r.OwnAddress == "" || peerID == r.NodeID {
		return false
	}
	ownParsed, err := url.Parse(r.OwnAddress)
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
	// TCP probe fail-fast on dead IPs without binding the HTTP body to a
	// short request context (that aborted large blob downloads).
	probeAddr := net.JoinHostPort(parsedAddr.Hostname(), defaultPortForURL(parsedAddr))
	conn, dialErr := net.DialTimeout("tcp", probeAddr, protocol.DialTimeoutRouteProbe)
	if dialErr != nil {
		return nil, dialErr
	}
	_ = conn.Close()

	resp, err := r.Base.RoundTrip(clone.Clone(clone.Context()))
	if err != nil {
		return nil, err
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert := resp.TLS.PeerCertificates[0]
		if vErr := VerifyPeerCN(cert, peerID); vErr != nil {
			_ = resp.Body.Close()
			r.logDebug("Rejecting direct connection: peer identity mismatch", "expected", peerID, "got", cert.Subject.CommonName)
			return nil, vErr
		}
	}
	return resp, nil
}

// errRelayPayloadTooLarge aborts routing outright: no other phase can carry a
// body the relay refuses.
var errRelayPayloadTooLarge = errors.New("payload exceeds relay size limit")

// tryRelay tunnels the request through the sponsor.
func (r *P2PRoundTripper) tryRelay(clone *http.Request, peerID, sponsorAddr string) (protocol.RelayResponse, error) {
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

func (r *P2PRoundTripper) tryExistingSession(clone *http.Request, req *http.Request, peerID string) (*http.Response, bool) {
	if r.QM == nil {
		return nil, false
	}
	sess, exists := r.QM.GetSession(peerID)
	if !exists {
		return nil, false
	}

	r.logDebug("Routing request directly over existing QUIC session", "peerID", peerID)
	return r.routeOverQUICSession(peerID, sess, req, clone)
}

func (r *P2PRoundTripper) routeOverQUICSession(peerID string, sess *quic.Conn, req, clone *http.Request) (*http.Response, bool) {
	resp, err := r.sendRequestOverQUIC(sess, req, clone)
	if err != nil {
		r.logDebug("Failed to send request over QUIC session", "peerID", peerID, "error", err)
		r.QM.CloseAndRemoveSession(peerID, 0x01, "transport error")
		return nil, false
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		r.QM.CloseAndRemoveSession(peerID, 0x02, "unauthorized")
	}
	return resp, true
}

func (r *P2PRoundTripper) tryHolePunchAndRoute(clone *http.Request, req *http.Request, peerID string, record protocol.AddressRecord, sponsorAddr string) (*http.Response, error, bool) {
	if r.QM == nil || sponsorAddr == "" {
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
	sess, err := r.QM.InitiateHolePunch(ctx, peerID, record.Addresses, sendRelayReq)
	cancel()
	if err != nil {
		r.logDebug("UDP Hole Punching failed", "peerID", peerID, "error", err)
		return nil, err, true
	}

	r.logDebug("UDP Hole Punching succeeded, routing over direct QUIC", "peerID", peerID)
	resp, ok := r.routeOverQUICSession(peerID, sess, req, clone)
	if !ok {
		return nil, fmt.Errorf("failed to send request over newly punched QUIC session"), true
	}
	return resp, nil, true
}

func (r *P2PRoundTripper) sendRequestOverQUIC(sess *quic.Conn, req *http.Request, clone *http.Request) (*http.Response, error) {
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
