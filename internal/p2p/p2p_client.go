package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
)

type PeerClient interface {
	FetchManifest(ctx context.Context, peerAddr string) (map[string]protocol.IndexEntry, error)
	Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]string, error)
	Notify(ctx context.Context, peerAddr string, notification protocol.PeerNotification) error
	AddPeer(peerAddr string, payload *bytes.Buffer) error
	DownloadBlob(ctx context.Context, peerAddr, hash string) (io.ReadCloser, error)
	DiscoverServices(ctx context.Context, peerAddr string) ([]string, error)
	ExecuteService(ctx context.Context, peerAddr string, serviceName string) (map[string]string, error)
	SubmitTask(ctx context.Context, peerAddr string, req protocol.TaskRequest) error
	SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error
	FetchServiceBid(ctx context.Context, peerAddr string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error)
}

type HTTPPeerClient struct {
	client *http.Client
}

func NewHTTPPeerClient(client *http.Client) *HTTPPeerClient {
	return &HTTPPeerClient{
		client: client,
	}
}

func validateAndBuildURL(base string, endpoint string) (string, error) {
	parsed, err := url.ParseRequestURI(base)
	if err != nil {
		return "", fmt.Errorf("invalid peer URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("insecure protocol blocked: %s", parsed.Scheme)
	}
	return url.JoinPath(parsed.String(), endpoint)
}

func (c *HTTPPeerClient) FetchManifest(ctx context.Context, peerAddr string) (map[string]protocol.IndexEntry, error) {
	safeURL, err := validateAndBuildURL(peerAddr, "manifest")
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

func (c *HTTPPeerClient) Notify(ctx context.Context, peerAddr string, notification protocol.PeerNotification) error {
	url := fmt.Sprintf("%s/notify", peerAddr)
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
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
func (c *HTTPPeerClient) DownloadBlob(ctx context.Context, peerAddr, hash string) (io.ReadCloser, error) {
	downloadURL := fmt.Sprintf("%s/download/%s", peerAddr, hash)
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
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

func (c *HTTPPeerClient) DiscoverServices(ctx context.Context, peerAddr string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", peerAddr+"/services", nil)
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

func (c *HTTPPeerClient) ExecuteService(ctx context.Context, peerAddr string, serviceName string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", peerAddr+"/services/execute?name="+serviceName, nil)
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

func (c *HTTPPeerClient) SubmitTask(ctx context.Context, peerAddr string, req protocol.TaskRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := peerAddr + "/services/submit"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
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

func (c *HTTPPeerClient) FetchServiceBid(ctx context.Context, peerAddr string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
    queryJSON, _ := json.Marshal(query)
    
    url := peerAddr + "/services/bid"
    req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(queryJSON))
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

func (c *HTTPPeerClient) AddPeer(peerAddr string, payload *bytes.Buffer) error {
	url := fmt.Sprintf("%s/peers/add", peerAddr)
	reqPeer, _ := http.NewRequest(http.MethodPost, url, payload)
	reqPeer.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(reqPeer)
	if err != nil {
		return fmt.Errorf("couldn't add peer for %s: %w", peerAddr, err)
	}
	err = resp.Body.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *HTTPPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]string, error) {
	url := fmt.Sprintf("%s/peers/announce", sponsorAddres)
	bodyBytes, _ := json.Marshal(peerRequest)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return map[string]string{}, fmt.Errorf("couldn't announce to %s: %w", sponsorAddres, err)
	}
	defer func(){ _ = resp.Body.Close() }()
	var peers map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return map[string]string{}, err
	}
	return peers, nil
}
