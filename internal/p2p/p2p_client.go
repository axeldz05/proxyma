package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"strings"
)

type PeerClient interface {
	FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error)
	Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error)
	Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error
	NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error
	AddPeer(peerID string, payload *bytes.Buffer) error
	DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error)
	DiscoverServices(ctx context.Context, peerID string) ([]string, error)
	ExecuteService(ctx context.Context, peerID string, serviceName string) (map[string]string, error)
	SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error
	SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error
	FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error)
	PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error)
	ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error
}

type HTTPPeerClient struct {
	client *http.Client
	router *P2PRoundTripper
}

func NewHTTPPeerClient(baseTransport http.RoundTripper, sponsorAddress string, logger *slog.Logger) *HTTPPeerClient {
	router := &P2PRoundTripper{
		Base:           baseTransport,
		SponsorAddress: sponsorAddress,
		Logger:         logger,
	}
	client := &http.Client{
		Transport: router,
	}
	return &HTTPPeerClient{
		client: client,
		router: router,
	}
}

func (c *HTTPPeerClient) UpdatePeerRoute(peerID string, record protocol.AddressRecord) {
	c.router.UpdatePeerRoute(peerID, record)
}

func (c *HTTPPeerClient) RemovePeerRoute(peerID string) {
	c.router.RemovePeerRoute(peerID)
}

func (c *HTTPPeerClient) UpdateSponsorAddress(addr string) {
	c.router.UpdateSponsorAddress(addr)
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

func (c *HTTPPeerClient) FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error) {
	safeURL, err := validateAndBuildURL(peerID, "manifest")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", safeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	var manifest map[string]protocol.IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (c *HTTPPeerClient) Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error {
	safeURL, err := validateAndBuildURL(peerID, "notify")
	if err != nil {
		return err
	}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return nil
}

func (c *HTTPPeerClient) NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error {
	safeURL, err := validateAndBuildURL(peerID, "services/notify")
	if err != nil {
		return err
	}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return nil
}

// If the returned error is nil, the [ReadCloser] is a non-nil Body which the user is expected to close.
// The Body should both be read to EOF and closed, otherwise it does not satisfy [Client] protocols
func (c *HTTPPeerClient) DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error) {
	safeURL, err := validateAndBuildURL(peerID, "download/"+hash)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", safeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() {
			_ = resp.Body.Close()
		}()
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *HTTPPeerClient) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	safeURL, err := validateAndBuildURL(peerID, "services")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", safeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	var svcs []string
	err = json.NewDecoder(resp.Body).Decode(&svcs)
	if err != nil {
		return nil, err
	}
	return svcs, nil
}

func (c *HTTPPeerClient) ExecuteService(ctx context.Context, peerID string, serviceName string) (map[string]string, error) {
	safeURL, err := validateAndBuildURL(peerID, "services/execute?name="+serviceName)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	var result map[string]string
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *HTTPPeerClient) SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	safeURL, err := validateAndBuildURL(peerID, "services/submit")
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", safeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("node is overloaded")
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

func (c *HTTPPeerClient) SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	return nil
}

func (c *HTTPPeerClient) FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
	queryJSON, _ := json.Marshal(query)

	safeURL, err := validateAndBuildURL(peerID, "services/bid")
	if err != nil {
		return protocol.ServiceBid{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeURL, bytes.NewReader(queryJSON))
	if err != nil {
		return protocol.ServiceBid{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return protocol.ServiceBid{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return protocol.ServiceBid{}, fmt.Errorf("peer returned status %d", resp.StatusCode)
	}

	var bid protocol.ServiceBid
	if err := json.NewDecoder(resp.Body).Decode(&bid); err != nil {
		return protocol.ServiceBid{}, err
	}

	return bid, nil
}

func (c *HTTPPeerClient) AddPeer(peerID string, payload *bytes.Buffer) error {
	safeURL, err := validateAndBuildURL(peerID, "peers/add")
	if err != nil {
		return err
	}
	reqPeer, _ := http.NewRequest(http.MethodPost, safeURL, payload)
	reqPeer.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(reqPeer)
	if err != nil {
		return fmt.Errorf("couldn't add peer for %s: %w", peerID, err)
	}
	err = resp.Body.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *HTTPPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
	safeURL, err := validateAndBuildURL(sponsorAddres, "peers/announce")
	if err != nil {
		return nil, err
	}
	bodyBytes, _ := json.Marshal(peerRequest)
	req, _ := http.NewRequest(http.MethodPost, safeURL, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return map[string]protocol.AddressRecord{}, fmt.Errorf("couldn't announce to %s: %w", sponsorAddres, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var peers map[string]protocol.AddressRecord
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return map[string]protocol.AddressRecord{}, err
	}
	return peers, nil
}

func (c *HTTPPeerClient) PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error) {
	safeURL, err := validateAndBuildURL(sponsorAddr, fmt.Sprintf("relay/poll?id=%s", peerID))
	if err != nil {
		return protocol.RelayRequest{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, safeURL, nil)
	if err != nil {
		return protocol.RelayRequest{}, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return protocol.RelayRequest{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return protocol.RelayRequest{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return protocol.RelayRequest{}, fmt.Errorf("sponsor returned status %d", resp.StatusCode)
	}

	var relayReq protocol.RelayRequest
	if err := json.NewDecoder(resp.Body).Decode(&relayReq); err != nil {
		return protocol.RelayRequest{}, err
	}

	return relayReq, nil
}

func (c *HTTPPeerClient) ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
	safeURL, err := validateAndBuildURL(sponsorAddr, "relay/reply")
	if err != nil {
		return err
	}

	bodyBytes, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, safeURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("sponsor returned status %d", httpResp.StatusCode)
	}

	return nil
}
