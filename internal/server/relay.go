package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

func (s *Server) getOrCreateQueue(peerID string) (chan protocol.RelayRequest, error) {
	if peerID != s.Config.ID {
		s.peersMu.RLock()
		_, exists := s.peers[peerID]
		s.peersMu.RUnlock()
		if !exists {
			return nil, fmt.Errorf("unknown peer ID: %s", peerID)
		}
	}

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	queue, exists := s.relayQueues[peerID]
	if !exists {
		queue = make(chan protocol.RelayRequest, 10)
		s.relayQueues[peerID] = queue
	}
	return queue, nil
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

	queue, err := s.getOrCreateQueue(peerID)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Wait for a request up to 30 seconds
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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

	queue, err := s.getOrCreateQueue(req.Target)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	waiter := s.registerRelayWaiter(req.ReqID)
	defer s.removeRelayWaiter(req.ReqID)

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

	waiter, exists := s.getRelayWaiter(resp.ReqID)
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

		relayReq, err := s.peerClient.PollRelay(ctx, sponsorAddr, s.Config.ID)
		if err != nil {
			// Network error, backoff
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
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

func (s *Server) registerRelayWaiter(reqID string) chan protocol.RelayResponse {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	waiter := make(chan protocol.RelayResponse, 1)
	s.relayWaiters[reqID] = waiter
	return waiter
}

func (s *Server) removeRelayWaiter(reqID string) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	delete(s.relayWaiters, reqID)
}

func (s *Server) getRelayWaiter(reqID string) (chan protocol.RelayResponse, bool) {
	s.relayMu.RLock()
	defer s.relayMu.RUnlock()
	waiter, exists := s.relayWaiters[reqID]
	return waiter, exists
}
