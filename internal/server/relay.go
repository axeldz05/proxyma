package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

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
	peerID := r.URL.Query().Get("id")
	if peerID == "" {
		utils.RespondError(w, http.StatusBadRequest, "Missing peer id")
		return
	}

	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		certCN := r.TLS.PeerCertificates[0].Subject.CommonName
		if certCN != peerID {
			utils.RespondError(w, http.StatusForbidden, "Unauthorized peer ID")
			return
		}
	}

	queue, err := s.Relays.GetOrCreateQueue(peerID)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Wait for a request up to 10 seconds
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
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
	req, err := utils.DecodeJSON[protocol.RelayRequest](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	if req.Target == "" || req.ReqID == "" {
		utils.RespondError(w, http.StatusBadRequest, "Missing target or req_id")
		return
	}

	// Security validation: if no valid peer certificates are supplied via mTLS,
	// only allow forwarding to the cluster joining endpoint.
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		if req.Path != "/cluster/join" {
			s.Config.Logger.Warn("Reject unauthenticated relay forward: path is not /cluster/join", "path", req.Path, "ip", r.RemoteAddr)
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

	// Wait for the response up to 60 seconds
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	select {
	case resp := <-waiter:
		utils.RespondJSON(w, http.StatusOK, resp)
	case <-ctx.Done():
		utils.RespondError(w, http.StatusGatewayTimeout, "Target did not respond in time")
	}
}

func (s *Server) HandleRelayReply(w http.ResponseWriter, r *http.Request) {
	resp, err := utils.DecodeJSON[protocol.RelayResponse](r)
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
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
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
	default:
		utils.RespondError(w, http.StatusInternalServerError, "Failed to deliver response")
	}
}

func (s *Server) StartRelayPolling(ctx context.Context, sponsorAddr string) {
	s.Config.Logger.Info("Starting Relay Polling", "sponsor", sponsorAddr)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 15*time.Second)
		relayReq, err := s.peerClient.PollRelay(pollCtx, sponsorAddr, s.Config.ID)
		pollCancel()
		if err != nil {
			s.Config.Logger.Debug("Relay poll failed", "error", err)
			// Network error, backoff
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		if relayReq.ReqID == "" {
			// Timeout reached without messages, poll again immediately
			continue
		}

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

	// Since this request is relayed and has already been verified/routed by the server,
	// we attach a mock TLS state to bypass the local mTLSGuard middleware checks.
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{}},
	}

	w := httptest.NewRecorder()
	// Route it through our own cached mux
	s.handler.ServeHTTP(w, req)

	res := w.Result()
	relayRes := protocol.RelayResponse{
		ReqID:      relayReq.ReqID,
		StatusCode: res.StatusCode,
		Headers:    make(map[string]string),
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	relayRes.Body = bodyBytes
	_ = res.Body.Close()

	for k, v := range res.Header {
		relayRes.Headers[k] = strings.Join(v, ",")
	}

	// Send reply back to Sponsor
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := s.peerClient.ReplyRelay(ctx, sponsorAddr, relayRes)
	if err != nil {
		s.Config.Logger.Error("Failed to reply to relay", "err", err, "reqID", relayReq.ReqID)
	}
}
