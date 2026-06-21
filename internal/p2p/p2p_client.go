package p2p

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	Leave(ctx context.Context, peerID string, leaveReq map[string]string) error
	Offline(ctx context.Context, peerID string, offlineReq map[string]string) error
	RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error)
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

func (c *HTTPPeerClient) FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error) {
	return doJSON[map[string]protocol.IndexEntry](ctx, c, "GET", peerID, "manifest", nil)
}

func (c *HTTPPeerClient) Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error {
	return doVoid(ctx, c, "POST", peerID, "notify", notification, 0)
}

func (c *HTTPPeerClient) NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error {
	return doVoid(ctx, c, "POST", peerID, "services/notify", notification, 0)
}

// If the returned error is nil, the [ReadCloser] is a non-nil Body which the user is expected to close.
// The Body should both be read to EOF and closed, otherwise it does not satisfy [Client] protocols
func (c *HTTPPeerClient) DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error) {
	resp, err := c.sendRequest(ctx, "GET", peerID, "download/"+hash, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *HTTPPeerClient) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return doJSON[[]string](ctx, c, "GET", peerID, "services", nil)
}

func (c *HTTPPeerClient) ExecuteService(ctx context.Context, peerID string, serviceName string) (map[string]string, error) {
	return doJSON[map[string]string](ctx, c, "POST", peerID, "services/execute?name="+serviceName, nil)
}

func (c *HTTPPeerClient) SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error {
	return doVoid(ctx, c, "POST", peerID, "services/submit?service="+req.Service, req, http.StatusAccepted)
}

func (c *HTTPPeerClient) SendTaskResponse(ctx context.Context, urlStr string, resp protocol.ServiceTaskResponse) error {
	importString := ""
	if strings.Contains(urlStr, "?") {
		importString = "&"
	} else {
		importString = "?"
	}
	return doVoid(ctx, c, "POST", urlStr+importString+"service="+resp.Service, "", resp, 0)
}

func (c *HTTPPeerClient) FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
	return doJSON[protocol.ServiceBid](ctx, c, "POST", peerID, "services/bid", query)
}

func (c *HTTPPeerClient) AddPeer(peerID string, payload *bytes.Buffer) error {
	return doVoid(context.Background(), c, "POST", peerID, "peers/add", payload, http.StatusOK)
}

func (c *HTTPPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
	return doJSON[map[string]protocol.AddressRecord](context.Background(), c, "POST", sponsorAddres, "peers/announce", peerRequest)
}

func (c *HTTPPeerClient) PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error) {
	return doJSON[protocol.RelayRequest](ctx, c, "GET", sponsorAddr, "relay/poll?id="+peerID, nil)
}

func (c *HTTPPeerClient) ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
	return doVoid(ctx, c, "POST", sponsorAddr, "relay/reply", resp, http.StatusOK)
}

func (c *HTTPPeerClient) Leave(ctx context.Context, peerID string, leaveReq map[string]string) error {
	return doVoid(ctx, c, "POST", peerID, "peers/leave", leaveReq, http.StatusOK)
}

func (c *HTTPPeerClient) Offline(ctx context.Context, peerID string, offlineReq map[string]string) error {
	return doVoid(ctx, c, "POST", peerID, "peers/offline", offlineReq, http.StatusOK)
}

func (c *HTTPPeerClient) RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error) {
	return doJSON[protocol.ProbeResponse](ctx, c, "POST", targetAddr, "peers/probe", req)
}
