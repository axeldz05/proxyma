package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type PeerClient interface {
	FetchManifest(ctx context.Context, peerAddr string) (map[string]IndexEntry, error)
	Notify(ctx context.Context, peerAddr string, notification PeerNotification) error
	DownloadBlob(ctx context.Context, peerAddr, hash string) (io.ReadCloser, error)
	GetSecret() string
	DiscoverServices(ctx context.Context, peerAddr string) ([]string, error)
	ExecuteService(ctx context.Context, peerAddr string, serviceName string) (map[string]string, error)
}

type HTTPPeerClient struct {
	client *http.Client
	secret string
}

func NewHTTPPeerClient(client *http.Client, secret string) *HTTPPeerClient {
	return &HTTPPeerClient{
		client: client,
		secret: secret,
	}
}

// TODO: Delete this function when implementing NodeConfig struct
func (c *HTTPPeerClient) GetSecret() string {
	return c.secret
}

func (c *HTTPPeerClient) FetchManifest(ctx context.Context, peerAddr string) (map[string]IndexEntry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", peerAddr+"/manifest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Proxyma-Secret", c.secret)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var manifest map[string]IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (c *HTTPPeerClient) Notify(ctx context.Context, peerAddr string, notification PeerNotification) error {
	url := fmt.Sprintf("%s/notify", peerAddr)
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Proxyma-Secret", c.secret)
	req.Header.Set("content-type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
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
	req.Header.Set("Proxyma-Secret", c.secret)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *HTTPPeerClient) DiscoverServices(ctx context.Context, peerAddr string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", peerAddr+"/services", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Proxyma-Secret", c.secret)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	req.Header.Set("Proxyma-Secret", c.secret)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]string
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

