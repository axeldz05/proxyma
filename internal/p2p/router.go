package p2p

import (
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
)

// P2PRoundTripper intercepts http://*.proxyma.local requests and attempts to route them
// either directly to the peer's known addresses or via a fallback Relay sponsor.
type P2PRoundTripper struct {
	mu             sync.RWMutex
	routes         map[string]protocol.AddressRecord
	SponsorAddress string
	Base           http.RoundTripper
	Logger         *slog.Logger
}

func (r *P2PRoundTripper) UpdatePeerRoute(peerID string, record protocol.AddressRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes == nil {
		r.routes = make(map[string]protocol.AddressRecord)
	}
	r.routes[peerID] = record
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
	
	var lastErr error
	// Phase 1: Direct Routing
	for _, rawAddr := range record.Addresses {
		parsedAddr, err := url.Parse(rawAddr)
		if err != nil {
			lastErr = err
			continue
		}

		clone.URL.Scheme = parsedAddr.Scheme
		clone.URL.Host = parsedAddr.Host
		
		// Attempt direct connection with a short timeout to fail-fast
		r.logDebug("Routing direct request", "url", clone.URL.String())
		directCtx, directCancel := context.WithTimeout(clone.Context(), 500*time.Millisecond)
		directReq := clone.Clone(directCtx)
		resp, err := r.Base.RoundTrip(directReq)
		directCancel()
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
			relayReq.Body = bodyBytes
			_ = clone.Body.Close()
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

func generateSecureReqID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano()) // Fallback
	}
	return hex.EncodeToString(bytes)
}
