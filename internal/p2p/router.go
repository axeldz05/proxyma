package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"strings"
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
	hasQuic := false
	for _, addr := range record.Addresses {
		if strings.HasPrefix(addr, "quic://") {
			hasQuic = true
			break
		}
	}
	if !hasQuic {
		return
	}

	r.mu.RLock()
	sponsorAddr := r.SponsorAddress
	r.mu.RUnlock()
	if sponsorAddr == "" {
		return
	}

	r.logDebug("Pre-warming direct QUIC connection to peer", "peerID", peerID)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
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
	relayReq := protocol.RelayRequest{
		ReqID:  generateSecureReqID(),
		Target: targetPeer,
		Method: http.MethodPost,
		Path:   path,
		Body:   body,
	}
	fwdBytes, _ := json.Marshal(relayReq)
	fwdReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sponsorAddr+"/relay/forward", bytes.NewBuffer(fwdBytes))
	if err != nil {
		return nil, err
	}
	fwdReq.Header.Set("Content-Type", "application/json")
	fwdResp, err := r.Base.RoundTrip(fwdReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fwdResp.Body.Close() }()
	if fwdResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", fwdResp.StatusCode)
	}
	var relayRes protocol.RelayResponse
	if err := json.NewDecoder(fwdResp.Body).Decode(&relayRes); err != nil {
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
	if !strings.HasSuffix(req.URL.Host, ".proxyma.local") {
		r.logDebug("Skipping routing: host does not match proxyma.local suffix", "host", req.URL.Host)
		return r.Base.RoundTrip(req)
	}
	r.logDebug("Host matches proxyma.local suffix, resolving peer", "host", req.URL.Host)

	peerID := strings.TrimSuffix(req.URL.Hostname(), ".proxyma.local")

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
	// Phase 1: Direct Routing
	for _, rawAddr := range record.Addresses {
		parsedAddr, err := url.Parse(rawAddr)
		if err != nil {
			lastErr = err
			continue
		}
		if parsedAddr.Scheme == "quic" {
			continue
		}

		host := parsedAddr.Hostname()
		port := parsedAddr.Port()
		if port == "" {
			if parsedAddr.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}

		if r.OwnAddress != "" {
			ownParsed, errOwn := url.Parse(r.OwnAddress)
			if errOwn == nil {
				ownPort := ownParsed.Port()
				if ownPort == "" {
					if ownParsed.Scheme == "https" {
						ownPort = "443"
					} else {
						ownPort = "80"
					}
				}

				isLoopbackTarget := host == "127.0.0.1" || host == "localhost" || host == "::1"

				if (isLoopbackTarget && port == ownPort) || (parsedAddr.Host == ownParsed.Host) {
					if peerID != r.NodeID {
						r.logDebug("Skipping loopback address that matches our own node host/port", "peerID", peerID, "host", host, "port", port)
						continue
					}
				}
			}
		}

		clone.URL.Scheme = parsedAddr.Scheme
		clone.URL.Host = parsedAddr.Host

		// Attempt direct connection with a short timeout to fail-fast
		r.logDebug("Routing direct request", "url", clone.URL.String())
		dCtx, dCancel := context.WithTimeout(clone.Context(), 2000*time.Millisecond)
		directReq := clone.Clone(dCtx)
		resp, err := r.Base.RoundTrip(directReq)
		if err == nil {
			resp.Body = NewCancelReadCloser(resp.Body, dCancel)
			return resp, nil
		}
		dCancel()
		lastErr = err
	}

	// Phase 1.5: UDP Hole Punching to establish QUIC session
	if resp, err, handled := r.tryHolePunchAndRoute(clone, req, peerID, record, sponsorAddr); handled {
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}

	// Phase 2: Relay Fallback
	if sponsorAddr != "" {
		relayReq := protocol.RelayRequest{
			ReqID:  generateSecureReqID(),
			Target: peerID,
			Method: clone.Method,
			Path:   clone.URL.Path,
		}
		if clone.Body != nil {
			bodyBytes, _ := io.ReadAll(clone.Body)
			_ = clone.Body.Close()
			if len(bodyBytes) > 65536 {
				return nil, fmt.Errorf("payload exceeds 64KB limit for relay fallback")
			}
			relayReq.Body = bodyBytes
		}
		relayReq.Headers = make(map[string]string)
		for k, v := range clone.Header {
			relayReq.Headers[k] = strings.Join(v, ",")
		}

		fwdBytes, _ := json.Marshal(relayReq)
		fwdReq, _ := http.NewRequestWithContext(clone.Context(), http.MethodPost, sponsorAddr+"/relay/forward", bytes.NewBuffer(fwdBytes))
		fwdReq.Header.Set("Content-Type", "application/json")

		fwdResp, err := r.Base.RoundTrip(fwdReq)
		if err == nil {
			if fwdResp.StatusCode == http.StatusOK {
				var relayRes protocol.RelayResponse
				if err := json.NewDecoder(fwdResp.Body).Decode(&relayRes); err == nil {
					_ = fwdResp.Body.Close()

					// Reconstruct the response
					res := &http.Response{
						StatusCode:    relayRes.StatusCode,
						Status:        http.StatusText(relayRes.StatusCode),
						Body:          io.NopCloser(bytes.NewReader(relayRes.Body)),
						Header:        make(http.Header),
						ContentLength: int64(len(relayRes.Body)),
						Request:       req,
					}
					for k, v := range relayRes.Headers {
						res.Header.Set(k, v)
					}
					return res, nil
				}
			}
			_ = fwdResp.Body.Close()
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to route to peer %s: %w", peerID, lastErr)
	}
	return nil, fmt.Errorf("no addresses available for peer %s", peerID)
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
	resp, err := r.sendRequestOverQUIC(sess, req, clone)
	if err != nil {
		r.logDebug("Failed to send request over existing QUIC session", "peerID", peerID, "error", err)
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
	for _, addr := range record.Addresses {
		if strings.HasPrefix(addr, "quic://") {
			hasQuicAddress = true
			break
		}
	}
	if !hasQuicAddress {
		return nil, nil, false
	}

	r.logDebug("Attempting UDP Hole Punching via Sponsor", "peerID", peerID)
	sendRelayReq := func(targetPeer, path string, body []byte) ([]byte, error) {
		return r.sendRelayMessage(clone.Context(), sponsorAddr, targetPeer, path, body)
	}

	holePunchTimeout := 3 * time.Second
	if deadline, ok := clone.Context().Deadline(); ok {
		timeLeft := time.Until(deadline)
		if timeLeft > 1000*time.Millisecond {
			holePunchTimeout = min(timeLeft-1000*time.Millisecond, 3*time.Second)
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
	resp, err := r.sendRequestOverQUIC(sess, req, clone)
	if err != nil {
		r.logDebug("Failed to send request over newly punched QUIC session", "peerID", peerID, "error", err)
		r.QM.CloseAndRemoveSession(peerID, 0x01, "failed")
		return nil, err, true
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		r.QM.CloseAndRemoveSession(peerID, 0x02, "unauthorized")
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

// CancelReadCloser wraps an io.ReadCloser to call a cancel function when closed.
type CancelReadCloser struct {
	io.ReadCloser
	Cancel context.CancelFunc
}

// Close closes the underlying ReadCloser and calls the Cancel function.
type CancelReadCloser_Close struct{} // dummy to help replace compile

func (c *CancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.Cancel()
	return err
}

// NewCancelReadCloser creates a new CancelReadCloser.
func NewCancelReadCloser(body io.ReadCloser, cancel context.CancelFunc) io.ReadCloser {
	return &CancelReadCloser{
		ReadCloser: body,
		Cancel:     cancel,
	}
}

func (r *P2PRoundTripper) CloseIdleConnections() {
	if idler, ok := r.Base.(interface{ CloseIdleConnections() }); ok {
		idler.CloseIdleConnections()
	}
}
