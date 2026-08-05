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
const DefaultRPCTimeout = 10 * time.Second

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
	return fmt.Sprintf("http://%s.proxyma.local/%s", peerID, path), nil
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
	return c.client.Do(req)
}

func doJSON[Resp any](ctx context.Context, c *HTTPPeerClient, method, target, path string, reqBody any) (Resp, error) {
	var respVal Resp
	bodyReader, contentType, err := prepareBody(reqBody)
	if err != nil {
		return respVal, err
	}
	if contentType == "" && reqBody != nil {
		contentType = "application/json"
	}

	resp, err := c.sendRequest(ctx, method, target, path, bodyReader, contentType)
	if err != nil {
		return respVal, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if !utils.HTTPSuccess(resp.StatusCode) {
		return respVal, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
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
	bodyReader, contentType, err := prepareBody(reqBody)
	if err != nil {
		return err
	}
	if contentType == "" && reqBody != nil {
		contentType = "application/json"
	}

	resp, err := c.sendRequest(ctx, method, target, path, bodyReader, contentType)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if expectedStatus != 0 {
		if resp.StatusCode != expectedStatus {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
	} else {
		if !utils.HTTPSuccess(resp.StatusCode) {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
	}

	return nil
}

// ForwardRelay POSTs a RelayRequest to sponsorAddr/relay/forward and decodes the response (L2).
func ForwardRelay(ctx context.Context, rt http.RoundTripper, sponsorAddr string, relayReq protocol.RelayRequest) (protocol.RelayResponse, error) {
	var zero protocol.RelayResponse
	if rt == nil {
		rt = http.DefaultTransport
	}
	fwdBytes, err := json.Marshal(relayReq)
	if err != nil {
		return zero, err
	}
	fwdReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sponsorAddr+"/relay/forward", bytes.NewBuffer(fwdBytes))
	if err != nil {
		return zero, err
	}
	fwdReq.Header.Set("Content-Type", "application/json")
	fwdResp, err := rt.RoundTrip(fwdReq)
	if err != nil {
		return zero, err
	}
	defer func() { _ = fwdResp.Body.Close() }()
	if fwdResp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("unexpected status code: %d", fwdResp.StatusCode)
	}
	var relayRes protocol.RelayResponse
	if err := json.NewDecoder(fwdResp.Body).Decode(&relayRes); err != nil {
		return zero, err
	}
	return relayRes, nil
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
	if idler, ok := b.Base.(interface{ CloseIdleConnections() }); ok {
		idler.CloseIdleConnections()
	}
}
