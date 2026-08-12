package p2p

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

// DefaultRPCTimeout is the shared timeout for short peer RPCs outside server.PeerRPC*.
const DefaultRPCTimeout = protocol.RPCTimeoutSync

// NewHTTPClient builds an http.Client with the given transport and timeout.
// A nil RoundTripper uses http.DefaultTransport; a zero timeout means no client-level deadline.
func NewHTTPClient(rt http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: rt,
		Timeout:   timeout,
	}
}
func validateAndBuildURL(peerID, path string) (string, error) {
	if peerID == "" {
		return "", fmt.Errorf("peer ID cannot be empty")
	}
	// Support direct URLs for fallback commands (like announce/SendTaskResponse)
	if u, err := url.Parse(peerID); err == nil && u.Scheme != "" && u.Host != "" {
		rel, err := url.Parse(path)
		if err != nil {
			return "", err
		}
		return u.ResolveReference(rel).String(), nil
	}
	path = strings.TrimPrefix(path, "/")
	return protocol.PeerHTTPURL(peerID, path), nil
}

func prepareBody(body any) (io.Reader, string, error) {
	if body == nil {
		return nil, "", nil
	}
	switch v := body.(type) {
	case io.Reader:
		return v, "", nil
	case []byte:
		return bytes.NewReader(v), "", nil
	default:
		bodyBytes, err := json.Marshal(v)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(bodyBytes), "application/json", nil
	}
}

func (c *HTTPPeerClient) sendRequest(ctx context.Context, method, target, path string, body io.Reader, contentType string) (*http.Response, error) {
	return c.sendRequestWithHeaders(ctx, method, target, path, body, contentType, nil)
}

func (c *HTTPPeerClient) sendRequestWithHeaders(
	ctx context.Context,
	method, target, path string,
	body io.Reader,
	contentType string,
	headers http.Header,
) (*http.Response, error) {
	safeURL, err := validateAndBuildURL(target, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, safeURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return c.client.Do(req)
}

// PostJSONAbsolute POSTs JSON to an absolute URL (L1). Caller must close the response body.
func PostJSONAbsolute(ctx context.Context, client *http.Client, urlStr string, body any) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	bodyReader, contentType, err := prepareBody(body)
	if err != nil {
		return nil, err
	}
	if contentType == "" && body != nil {
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return client.Do(req)
}

func doRequest(ctx context.Context, c *HTTPPeerClient, method, target, path string, reqBody any) (*http.Response, error) {
	bodyReader, contentType, err := prepareBody(reqBody)
	if err != nil {
		return nil, err
	}
	if contentType == "" && reqBody != nil {
		contentType = "application/json"
	}
	return c.sendRequest(ctx, method, target, path, bodyReader, contentType)
}

func doJSON[Resp any](ctx context.Context, c *HTTPPeerClient, method, target, path string, reqBody any) (Resp, error) {
	var respVal Resp
	resp, err := doRequest(ctx, c, method, target, path, reqBody)
	if err != nil {
		return respVal, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if !utils.HTTPSuccess(resp.StatusCode) {
		return respVal, utils.HTTPStatusError(resp.StatusCode)
	}

	if resp.StatusCode == http.StatusNoContent {
		return respVal, nil
	}

	if err := json.NewDecoder(resp.Body).Decode(&respVal); err != nil {
		return respVal, err
	}

	return respVal, nil
}

func doVoid(ctx context.Context, c *HTTPPeerClient, method, target, path string, reqBody any, expectedStatus int) error {
	resp, err := doRequest(ctx, c, method, target, path, reqBody)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return RequireHTTPStatus(resp, expectedStatus)
}

// RequireHTTPStatus checks a response status without consuming the body (L1).
// wantStatus 0 accepts any 2xx.
func RequireHTTPStatus(resp *http.Response, wantStatus int) error {
	if wantStatus != 0 {
		if resp.StatusCode != wantStatus {
			return utils.HTTPStatusError(resp.StatusCode)
		}
		return nil
	}
	if !utils.HTTPSuccess(resp.StatusCode) {
		return utils.HTTPStatusError(resp.StatusCode)
	}
	return nil
}

// OpenHTTPBody hands the caller a body to stream, closing it when the status is
// not the expected one (L1). Used by the binary/NDJSON paths that skip doJSON.
func OpenHTTPBody(resp *http.Response, wantStatus int) (io.ReadCloser, error) {
	if err := RequireHTTPStatus(resp, wantStatus); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// ForwardRelay POSTs a RelayRequest to sponsorAddr + PathRelayForward and decodes the response (L2).
func ForwardRelay(ctx context.Context, rt http.RoundTripper, sponsorAddr string, relayReq protocol.RelayRequest) (protocol.RelayResponse, error) {
	var zero protocol.RelayResponse
	client := NewHTTPClient(rt, 0)
	fwdResp, err := PostJSONAbsolute(ctx, client, sponsorAddr+protocol.PathRelayForward, relayReq)
	if err != nil {
		return zero, err
	}
	defer func() { _ = fwdResp.Body.Close() }()
	if err := RequireHTTPStatus(fwdResp, http.StatusOK); err != nil {
		return zero, err
	}
	var relayRes protocol.RelayResponse
	if err := json.NewDecoder(fwdResp.Body).Decode(&relayRes); err != nil {
		return zero, err
	}
	return relayRes, nil
}

// NewRelayRequest builds a RelayRequest with a secure ReqID (L2).
func NewRelayRequest(target, method, path string, body []byte, headers map[string]string) protocol.RelayRequest {
	return protocol.NewRelayRequest(generateSecureReqID(), target, method, path, body, headers)
}

// RequestPathWithQuery returns path, or path?query when RawQuery is set (L1).
func RequestPathWithQuery(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// FlattenHTTPHeader converts http.Header to map[string]string joining multi-values with commas (L1).
func FlattenHTTPHeader(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ",")
	}
	return out
}

// QUICScheme is the address scheme for QUIC/UDP hole-punch endpoints.
const QUICScheme = "quic://"

// FormatQUICAddr builds a quic://host:port address (L1).
func FormatQUICAddr(hostport string) string {
	return QUICScheme + hostport
}

// ParseQUICAddr strips the quic:// scheme. ok is false if addr is not a QUIC address.
func ParseQUICAddr(addr string) (hostport string, ok bool) {
	if !strings.HasPrefix(addr, QUICScheme) {
		return "", false
	}
	hostport = strings.TrimPrefix(addr, QUICScheme)
	return hostport, hostport != ""
}

// FirstQUICAddr returns the first quic:// address in the list.
func FirstQUICAddr(addrs []string) (string, bool) {
	for _, addr := range addrs {
		if _, ok := ParseQUICAddr(addr); ok {
			return addr, true
		}
	}
	return "", false
}

// VerifyPeerCN checks that cert CommonName matches expectedPeerID.
func VerifyPeerCN(cert *x509.Certificate, expectedPeerID string) error {
	if cert == nil {
		return fmt.Errorf("peer identity mismatch: missing certificate")
	}
	if cert.Subject.CommonName != expectedPeerID {
		return fmt.Errorf("peer identity mismatch: expected %s, got %s", expectedPeerID, cert.Subject.CommonName)
	}
	return nil
}

// VerifyTLSPeerCN extracts the leaf CN from state and verifies it against expectedPeerID (L2).
func VerifyTLSPeerCN(state *tls.ConnectionState, expectedPeerID string) error {
	cn, ok := PeerCNFromTLS(state)
	if !ok {
		return fmt.Errorf("peer identity mismatch: missing certificate")
	}
	if cn != expectedPeerID {
		return fmt.Errorf("peer identity mismatch: expected %s, got %s", expectedPeerID, cn)
	}
	return nil
}

// BandwidthRecorder abstracts bandwidth tracking to prevent circular dependencies.
type BandwidthRecorder interface {
	RecordBytesSent(n int64, path string)
	RecordBytesReceived(n int64, path string)
}

// BandwidthRoundTripper intercepts HTTP requests and responses to count bandwidth.
type BandwidthRoundTripper struct {
	Base     http.RoundTripper
	Recorder BandwidthRecorder
}

func (b *BandwidthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && b.Recorder != nil {
		req.Body = &utils.CountingReadCloser{
			ReadCloser: req.Body,
			OnRead: func(n int) {
				b.Recorder.RecordBytesSent(int64(n), req.URL.RequestURI())
			},
		}
	}

	resp, err := b.Base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil && b.Recorder != nil {
		resp.Body = &utils.CountingReadCloser{
			ReadCloser: resp.Body,
			OnRead: func(n int) {
				b.Recorder.RecordBytesReceived(int64(n), req.URL.RequestURI())
			},
		}
	}

	return resp, nil
}

func (b *BandwidthRoundTripper) CloseIdleConnections() {
	closeIdle(b.Base)
}

func closeIdle(rt http.RoundTripper) {
	if idler, ok := rt.(interface{ CloseIdleConnections() }); ok {
		idler.CloseIdleConnections()
	}
}
