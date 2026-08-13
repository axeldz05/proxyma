package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
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

const maxRelayJSONBytes = int64(2 * protocol.MaxRelayBodyBytes)
const relayQueueSize = 10

var errRelayResponseTooLarge = errors.New("relay response body exceeds limit")

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

// decodeRelayJSON bounds the encoded JSON envelope before decoding. The
// envelope allowance covers base64 expansion of a maximum-size Body plus
// ordinary request metadata.
func decodeRelayJSON[T any](w http.ResponseWriter, r *http.Request, what string) (T, bool) {
	var payload T
	if r.ContentLength > maxRelayJSONBytes {
		utils.RespondError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("%s JSON exceeds %dKB encoded limit", what, maxRelayJSONBytes/1024))
		return payload, false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRelayJSONBytes)
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err == nil {
		return payload, true
	}

	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		utils.RespondError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("%s JSON exceeds %dKB encoded limit", what, maxRelayJSONBytes/1024))
	} else {
		utils.RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
	}
	return payload, false
}

// cappedRelayResponseWriter stops local handlers while they write past the
// relay cap, keeping the recorder itself bounded.
type cappedRelayResponseWriter struct {
	recorder *httptest.ResponseRecorder
	written  int
	exceeded bool
}

func newCappedRelayResponseWriter() *cappedRelayResponseWriter {
	return &cappedRelayResponseWriter{recorder: httptest.NewRecorder()}
}

func (w *cappedRelayResponseWriter) Header() http.Header {
	return w.recorder.Header()
}

func (w *cappedRelayResponseWriter) WriteHeader(statusCode int) {
	w.recorder.WriteHeader(statusCode)
}

func (w *cappedRelayResponseWriter) Write(p []byte) (int, error) {
	remaining := protocol.MaxRelayBodyBytes - w.written
	if len(p) <= remaining {
		n, err := w.recorder.Write(p)
		w.written += n
		return n, err
	}

	w.exceeded = true
	if remaining == 0 {
		return 0, errRelayResponseTooLarge
	}
	n, err := w.recorder.Write(p[:remaining])
	w.written += n
	if err != nil {
		return n, err
	}
	return n, errRelayResponseTooLarge
}

// Flush preserves the interface exposed by httptest.ResponseRecorder.
func (w *cappedRelayResponseWriter) Flush() {
	w.recorder.Flush()
}

// RelayManager manages the relay communication queues and response waiters for tunneling HTTP requests.
type RelayManager struct {
	server  *Server
	queues  map[string]chan protocol.RelayRequest
	waiters map[string]*relayWaiter
	mu      sync.RWMutex

	workSlots chan struct{}
}

type relayWaiter struct {
	ch           chan protocol.RelayResponse
	expectedPeer string
}

// NewRelayManager creates a new RelayManager.
func NewRelayManager(server *Server) *RelayManager {
	return &RelayManager{
		server:    server,
		queues:    make(map[string]chan protocol.RelayRequest),
		waiters:   make(map[string]*relayWaiter),
		workSlots: make(chan struct{}, relayQueueSize),
	}
}

func (rm *RelayManager) lifetime() context.Context {
	if rm.server != nil && rm.server.lifetimeCtx != nil {
		return rm.server.lifetimeCtx
	}
	return context.Background()
}

func (rm *RelayManager) beginWork(ctx context.Context) (finish func(), ok bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, false
	case <-rm.lifetime().Done():
		return nil, false
	case rm.workSlots <- struct{}{}:
	}

	if rm.lifetime().Err() != nil {
		<-rm.workSlots
		return nil, false
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-rm.workSlots
		})
	}, true
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
		queue = make(chan protocol.RelayRequest, relayQueueSize)
		rm.queues[peerID] = queue
	}
	return queue, nil
}

// RegisterWaiter creates and registers a response waiter channel for a request ID.
// expectedPeer is the only CN allowed to deliver HandleRelayReply for this ReqID.
func (rm *RelayManager) RegisterWaiter(reqID, expectedPeer string) chan protocol.RelayResponse {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	waiter := &relayWaiter{
		ch:           make(chan protocol.RelayResponse, 1),
		expectedPeer: expectedPeer,
	}
	rm.waiters[reqID] = waiter
	return waiter.ch
}

// RemoveWaiter unregisters a response waiter channel.
func (rm *RelayManager) RemoveWaiter(reqID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.waiters, reqID)
}

// GetWaiter retrieves a registered response waiter and the peer CN allowed to reply.
func (rm *RelayManager) GetWaiter(reqID string) (ch chan protocol.RelayResponse, expectedPeer string, ok bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	waiter, exists := rm.waiters[reqID]
	if !exists {
		return nil, "", false
	}
	return waiter.ch, waiter.expectedPeer, true
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
	req, ok := decodeRelayJSON[protocol.RelayRequest](w, r, "Relay request")
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

	waiter := s.Relays.RegisterWaiter(req.ReqID, req.Target)
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
	resp, ok := decodeRelayJSON[protocol.RelayResponse](w, r, "Relay response")
	if !ok {
		return
	}

	if rejectOversizedRelay(w, len(resp.Body), "Relay response") {
		return
	}

	cn, ok := peerCNFromRequest(r)
	if !ok {
		forbidMissingMTLS(w)
		return
	}

	waiter, expectedPeer, exists := s.Relays.GetWaiter(resp.ReqID)
	if !exists {
		utils.RespondError(w, http.StatusNotFound, "ReqID not found or expired")
		return
	}
	if expectedPeer != "" && cn != expectedPeer {
		s.Config.Logger.Warn("Reject relay reply: CN does not match target peer for ReqID",
			"reqID", resp.ReqID, "cn", cn, "expected", expectedPeer)
		utils.RespondError(w, http.StatusForbidden, "certificate CN must match relay target peer")
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
	lifetime := s.Relays.lifetime()
	select {
	case <-ctx.Done():
		return
	case <-lifetime.Done():
		return
	default:
	}
	s.peerClient.UpdateSponsorAddress(sponsorAddr)
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
		case <-lifetime.Done():
			return
		default:
		}

		if currentSleep > 0 {
			select {
			case <-ctx.Done():
				return
			case <-lifetime.Done():
				return
			case <-time.After(currentSleep):
			}
		}

		finishWork, ok := s.Relays.beginWork(ctx)
		if !ok {
			return
		}
		pollCtx, pollCancel := context.WithTimeout(ctx, PeerRPCRelayTick)
		stopLifetimeCancel := context.AfterFunc(lifetime, pollCancel)
		relayReq, err := s.peerClient.PollRelay(pollCtx, sponsorAddr, s.Config.ID)
		stopLifetimeCancel()
		pollCancel()
		if err != nil {
			finishWork()
			s.Config.Logger.Debug("Relay poll failed, trying to failover", "error", err)

			s.peerClient.CloseIdleConnections()

			// Try to find another sponsor from GetSponsorPeers()
			sponsors := s.GetSponsorPeers()
			if len(sponsors) > 0 {
				nextSponsor := ""
				fallback := false
				for id, addr := range sponsors {
					if addr != sponsorAddr && s.IsPeerOnline(id) {
						nextSponsor = addr
						break
					}
				}
				if nextSponsor == "" {
					for _, addr := range sponsors {
						if addr != sponsorAddr {
							nextSponsor = addr
							fallback = true
							break
						}
					}
				}
				if nextSponsor != "" {
					if fallback {
						s.Config.Logger.Info("Switching relay sponsor (fallback to offline/any)", "old", sponsorAddr, "new", nextSponsor)
					} else {
						s.Config.Logger.Info("Switching relay sponsor", "old", sponsorAddr, "new", nextSponsor)
					}
					// Keep outbound relay and hole-punch routing synchronized with
					// the sponsor used by this polling loop.
					s.peerClient.UpdateSponsorAddress(nextSponsor)
					sponsorAddr = nextSponsor
				}
			}

			// Network error backoff
			select {
			case <-ctx.Done():
				return
			case <-lifetime.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		if relayReq.ReqID == "" {
			finishWork()
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
		requestSponsor, request := sponsorAddr, relayReq
		if !s.goOwned(func() {
			defer finishWork()
			s.processRelayRequestContext(s.lifetimeCtx, requestSponsor, request)
		}) {
			finishWork()
			return
		}
	}
}

func (s *Server) processRelayRequest(sponsorAddr string, relayReq protocol.RelayRequest) {
	s.processRelayRequestContext(context.Background(), sponsorAddr, relayReq)
}

func (s *Server) processRelayRequestContext(ctx context.Context, sponsorAddr string, relayReq protocol.RelayRequest) {
	if relayCapExceeded(len(relayReq.Body)) {
		s.sendRelayResponseContext(ctx, sponsorAddr, protocol.RelayResponse{
			ReqID:      relayReq.ReqID,
			StatusCode: http.StatusRequestEntityTooLarge,
			Body:       []byte(fmt.Sprintf(`{"error":%q}`, relayCapMessage("relay payload"))),
		})
		return
	}

	// Reconstruct the HTTP request and pass it to our own local HTTP handler
	// (Simulate an incoming HTTP request)
	reqURL := fmt.Sprintf("http://127.0.0.1%s", relayReq.Path)
	req, err := http.NewRequestWithContext(ctx, relayReq.Method, reqURL, bytes.NewBuffer(relayReq.Body))
	if err != nil {
		s.Config.Logger.Warn("Reject malformed relay request", "reqID", relayReq.ReqID, "error", err)
		s.sendRelayResponseContext(ctx, sponsorAddr, protocol.RelayResponse{
			ReqID:      relayReq.ReqID,
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":"invalid relay request"}`),
		})
		return
	}
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

	w := newCappedRelayResponseWriter()
	// Route it through our own cached mux
	s.handler.ServeHTTP(w, req)

	res := w.recorder.Result()
	relayRes := protocol.RelayResponse{
		ReqID:      relayReq.ReqID,
		StatusCode: res.StatusCode,
		Headers:    p2p.FlattenHTTPHeader(res.Header),
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if w.exceeded {
		s.Config.Logger.Warn("Relay response body exceeds cap; replacing with error", "reqID", relayReq.ReqID)
		relayRes.StatusCode = http.StatusRequestEntityTooLarge
		relayRes.Body = []byte(fmt.Sprintf(`{"error":%q}`, relayCapMessage("relay response")))
	} else {
		relayRes.Body = bodyBytes
	}

	s.sendRelayResponseContext(ctx, sponsorAddr, relayRes)
}

func (s *Server) sendRelayResponseContext(parent context.Context, sponsorAddr string, relayRes protocol.RelayResponse) {
	ctx, cancel := context.WithTimeout(parent, PeerRPCSync)
	defer cancel()

	err := s.peerClient.ReplyRelay(ctx, sponsorAddr, relayRes)
	if err != nil {
		s.Config.Logger.Error("Failed to reply to relay", "err", err, "reqID", relayRes.ReqID)
	}
}
