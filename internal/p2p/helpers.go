package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"proxyma/internal/utils"
	"strings"
)

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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
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

