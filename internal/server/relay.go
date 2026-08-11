package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// relayCapExceeded reports whether size is over the relay body cap (L1).
func relayCapExceeded(size int) bool {
	return size > protocol.MaxRelayBodyBytes
}

// relayCapMessage renders the cap error text from the constant, so raising
// MaxRelayBodyBytes never leaves a stale size in the message.
func relayCapMessage(what string) string {
	return fmt.Sprintf("%s exceeds %dKB limit", what, protocol.MaxRelayBodyBytes/1024)
}

// rejectOversizedRelay answers 413 when size is over the cap, reporting whether
// the caller should stop (L1).
func rejectOversizedRelay(w http.ResponseWriter, size int, what string) bool {
	if !relayCapExceeded(size) {
		return false
	}
	utils.RespondError(w, http.StatusRequestEntityTooLarge, relayCapMessage(what))
	return true
}

// RelayManager manages the relay communication queues and response waiters for tunneling HTTP requests.
type RelayManager struct {
	server  *Server
	queues  map[string]chan protocol.RelayRequest
	waiters map[string]chan protocol.RelayResponse
	mu      sync.RWMutex
}

// NewRelayManager creates a new RelayManager.
func NewRelayManager(server *Server) *RelayManager {
	return &RelayManager{
		server:  server,
		queues:  make(map[string]chan protocol.RelayRequest),
		waiters: make(map[string]chan protocol.RelayResponse),
	}
}

// GetOrCreateQueue returns or initializes the request queue for a peer.
func (rm *RelayManager) GetOrCreateQueue(peerID string) (chan protocol.RelayRequest, error) {
	if peerID != rm.server.Config.ID {
		if _, exists := rm.server.Peers.GetPeerRecord(peerID); !exists {
			return nil, fmt.Errorf("unknown peer ID: %s", peerID)
		}
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	queue, exists := rm.queues[peerID]
	if !exists {
		queue = make(chan protocol.RelayRequest, 10)
		rm.queues[peerID] = queue
	}
	return queue, nil
}

// RegisterWaiter creates and registers a response waiter channel for a request ID.
func (rm *RelayManager) RegisterWaiter(reqID string) chan protocol.RelayResponse {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	waiter := make(chan protocol.RelayResponse, 1)
	rm.waiters[reqID] = waiter
	return waiter
}

// RemoveWaiter unregisters a response waiter channel.
func (rm *RelayManager) RemoveWaiter(reqID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.waiters, reqID)
}

// GetWaiter retrieves a registered response waiter channel.
func (rm *RelayManager) GetWaiter(reqID string) (chan protocol.RelayResponse, bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	waiter, exists := rm.waiters[reqID]
	return waiter, exists
}

func (s *Server) HandleRelayPoll(w http.ResponseWriter, r *http.Request) {
	peerID, ok := utils.GetRequiredQueryParam(w, r, "id")
	if !ok {
		return
	}

	if certCN, ok := peerCNFromRequest(r); ok && certCN != peerID {
		utils.RespondError(w, http.StatusForbidden, "Unauthorized peer ID")
		return
	}

	queue, err := s.Relays.GetOrCreateQueue(peerID)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Wait for a request up to 10 seconds
	ctx, cancel := context.WithTimeout(r.Context(), PeerRPCSync)
	defer cancel()

	select {
	case req := <-queue:
		utils.RespondJSON(w, http.StatusOK, req)
	case <-ctx.Done():
		// Timeout, no requests. Return 204 No Content
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) HandleRelayForward(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONOrError[protocol.RelayRequest](w, r)
	if !ok {
		return
	}

	if rejectOversizedRelay(w, len(req.Body), "Relay payload") {
		return
	}

	if req.Target == "" || req.ReqID == "" {
		utils.RespondError(w, http.StatusBadRequest, "Missing target or req_id")
		return
	}

	// Security validation: if no valid peer certificates are supplied via mTLS,
	// only allow forwarding to the cluster joining endpoint.
	if cn, ok := peerCNFromRequest(r); ok {
		if req.OriginPeerID == "" {
			req.OriginPeerID = cn
		} else if req.OriginPeerID != cn {
			utils.RespondError(w, http.StatusForbidden, "OriginPeerID must match authenticated peer CN")
			return
		}
	} else {
		if !s.relayAllowsAnonymous(req.Path) {
			s.Config.Logger.Warn("Reject unauthenticated relay forward: path is not relay-anonymous", "path", req.Path, "ip", r.RemoteAddr)
			utils.RespondError(w, http.StatusForbidden, "mTLS certificate required for this relay path")
			return
		}
	}

	queue, err := s.Relays.GetOrCreateQueue(req.Target)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	waiter := s.Relays.RegisterWaiter(req.ReqID)
	defer s.Relays.RemoveWaiter(req.ReqID)

	// Send to queue (non-blocking if full)
	select {
	case queue <- req:
	default:
		utils.RespondError(w, http.StatusServiceUnavailable, "Target queue is full")
		return
	}

	// Wait for the response up to PeerRPCRelayHold
	ctx, cancel := context.WithTimeout(r.Context(), PeerRPCRelayHold)
	defer cancel()

	select {
	case resp := <-waiter:
		if rejectOversizedRelay(w, len(resp.Body), "Relay response") {
			return
		}
		utils.RespondJSON(w, http.StatusOK, resp)
	case <-ctx.Done():
		utils.RespondError(w, http.StatusGatewayTimeout, "Target did not respond in time")
	}
}

func (s *Server) HandleRelayReply(w http.ResponseWriter, r *http.Request) {
	resp, ok := utils.DecodeJSONOrError[protocol.RelayResponse](w, r)
	if !ok {
		return
	}

	if rejectOversizedRelay(w, len(resp.Body), "Relay response") {
		return
	}

	waiter, exists := s.Relays.GetWaiter(resp.ReqID)
	if !exists {
		utils.RespondError(w, http.StatusNotFound, "ReqID not found or expired")
		return
	}

	// Send the response to the waiting forward handler
	select {
	case waiter <- resp:
		utils.RespondStatus(w, http.StatusOK, "delivered")
	default:
		utils.RespondError(w, http.StatusInternalServerError, "Failed to deliver response")
	}
}

func (s *Server) StartRelayPolling(ctx context.Context, sponsorAddr string) {
	s.Config.Logger.Info("Starting Relay Polling", "sponsor", sponsorAddr)

	minInterval := time.Duration(s.Config.MinRelayPollInterval) * time.Second
	if minInterval <= 0 {
		minInterval = 2 * time.Second
	}
	maxInterval := time.Duration(s.Config.MaxRelayPollInterval) * time.Second
	if maxInterval <= 0 {
		maxInterval = 45 * time.Second
	}

	currentSleep := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if currentSleep > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(currentSleep):
			}
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, PeerRPCRelayTick)
		relayReq, err := s.peerClient.PollRelay(pollCtx, sponsorAddr, s.Config.ID)
		pollCancel()
		if err != nil {
			s.Config.Logger.Debug("Relay poll failed, trying to failover", "error", err)

			s.peerClient.CloseIdleConnections()

			// Try to find another sponsor from GetSponsorPeers()
			sponsors := s.GetSponsorPeers()
			if len(sponsors) > 0 {
				found := false
				for id, addr := range sponsors {
					if addr != sponsorAddr && s.IsPeerOnline(id) {
						s.Config.Logger.Info("Switching relay sponsor", "old", sponsorAddr, "new", addr)
						sponsorAddr = addr
						found = true
						break
					}
				}
				if !found {
					for _, addr := range sponsors {
						if addr != sponsorAddr {
							s.Config.Logger.Info("Switching relay sponsor (fallback to offline/any)", "old", sponsorAddr, "new", addr)
							sponsorAddr = addr
							break
						}
					}
				}
			}

			// Network error backoff
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		if relayReq.ReqID == "" {
			// Timeout reached without messages (204 No Content)
			// Increase sleep interval adaptively
			if currentSleep == 0 {
				currentSleep = minInterval
			} else {
				currentSleep += 2 * time.Second
				if currentSleep > maxInterval {
					currentSleep = maxInterval
				}
			}
			continue
		}

		// Message received! Reset sleep for instant responsiveness
		currentSleep = 0

		// Process the request asynchronously so we can keep polling
		go s.processRelayRequest(sponsorAddr, relayReq)
	}
}

func (s *Server) processRelayRequest(sponsorAddr string, relayReq protocol.RelayRequest) {
	// Reconstruct the HTTP request and pass it to our own local HTTP handler
	// (Simulate an incoming HTTP request)
	reqURL := fmt.Sprintf("http://127.0.0.1%s", relayReq.Path)
	req, _ := http.NewRequest(relayReq.Method, reqURL, bytes.NewBuffer(relayReq.Body))
	for k, v := range relayReq.Headers {
		req.Header.Set(k, v)
	}

	// Preserve originator identity from the sponsor; do not forge self-cert for auth.
	if relayReq.OriginPeerID != "" {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{
				Subject: pkix.Name{CommonName: relayReq.OriginPeerID},
			}},
		}
	}

	w := httptest.NewRecorder()
	// Route it through our own cached mux
	s.handler.ServeHTTP(w, req)

	res := w.Result()
	relayRes := protocol.RelayResponse{
		ReqID:      relayReq.ReqID,
		StatusCode: res.StatusCode,
		Headers:    p2p.FlattenHTTPHeader(res.Header),
	}
	limited := io.LimitReader(res.Body, int64(protocol.MaxRelayBodyBytes)+1)
	bodyBytes, _ := io.ReadAll(limited)
	_ = res.Body.Close()
	if relayCapExceeded(len(bodyBytes)) {
		s.Config.Logger.Warn("Relay response body exceeds cap; truncating to error", "reqID", relayReq.ReqID, "size", len(bodyBytes))
		relayRes.StatusCode = http.StatusRequestEntityTooLarge
		relayRes.Body = []byte(fmt.Sprintf(`{"error":%q}`, relayCapMessage("relay response")))
	} else {
		relayRes.Body = bodyBytes
	}

	// Send reply back to Sponsor
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCSync)
	defer cancel()

	err := s.peerClient.ReplyRelay(ctx, sponsorAddr, relayRes)
	if err != nil {
		s.Config.Logger.Error("Failed to reply to relay", "err", err, "reqID", relayReq.ReqID)
	}
}
