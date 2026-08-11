package p2p

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"proxyma/internal/protocol"
)

type PeerClient interface {
	FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error)
	Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error)
	Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error
	NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error
	NotifyPipelineSchema(ctx context.Context, peerID string, notification protocol.PipelineNotification) error
	AddPeer(peerID string, req protocol.AddPeerRequest) error
	DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error)
	DiscoverServices(ctx context.Context, peerID string) ([]string, error)
	SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error
	SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error
	FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error)
	PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error)
	ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error
	Leave(ctx context.Context, peerID string, leaveReq protocol.PeerIDRequest) error
	Offline(ctx context.Context, peerID string, offlineReq protocol.PeerIDRequest) error
	RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error)
	RotateTLS(ctx context.Context, peerID string, payload protocol.RotateTLSPayload) error
	StreamService(ctx context.Context, peerID string, serviceName string, payload map[string]any) (io.ReadCloser, error)

	// Routing / lifecycle (formerly ad-hoc type assertions)
	UpdatePeerRoute(peerID string, record protocol.AddressRecord)
	RemovePeerRoute(peerID string)
	SetNodeID(id string)
	SetOwnAddress(addr string)
	UpdateSponsorAddress(addr string)
	CloseIdleConnections()
	SetQUICManager(qm *QUICManager)
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
	client := NewHTTPClient(router, 0)
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

func (c *HTTPPeerClient) SetNodeID(id string) {
	c.router.NodeID = id
}

func (c *HTTPPeerClient) SetOwnAddress(addr string) {
	c.router.OwnAddress = addr
}

func (c *HTTPPeerClient) UpdateSponsorAddress(addr string) {
	c.router.UpdateSponsorAddress(addr)
}

func (c *HTTPPeerClient) SetQUICManager(qm *QUICManager) {
	c.router.QM = qm
}

func (c *HTTPPeerClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
}

func (c *HTTPPeerClient) FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error) {
	return doJSON[map[string]protocol.IndexEntry](ctx, c, "GET", peerID, protocol.PathRel(protocol.PathManifest), nil)
}

func (c *HTTPPeerClient) Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathNotify), notification, 0)
}

func (c *HTTPPeerClient) NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathServicesNotify), notification, 0)
}

func (c *HTTPPeerClient) NotifyPipelineSchema(ctx context.Context, peerID string, notification protocol.PipelineNotification) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathSchemasNotify), notification, 0)
}

// If the returned error is nil, the [ReadCloser] is a non-nil Body which the user is expected to close.
// The Body should both be read to EOF and closed, otherwise it does not satisfy [Client] protocols
func (c *HTTPPeerClient) DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error) {
	resp, err := c.sendRequest(ctx, "GET", peerID, protocol.PathRel(protocol.PathDownloadPrefix)+hash, nil, "")
	if err != nil {
		return nil, err
	}
	return OpenHTTPBody(resp, http.StatusOK)
}

func (c *HTTPPeerClient) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return doJSON[[]string](ctx, c, "GET", peerID, protocol.PathRel(protocol.PathServices), nil)
}

func (c *HTTPPeerClient) SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error {
	return doVoid(ctx, c, "POST", peerID, protocol.WithServiceQuery(protocol.PathRel(protocol.PathServicesSubmit), req.Service), req, http.StatusAccepted)
}

func (c *HTTPPeerClient) SendTaskResponse(ctx context.Context, urlStr string, resp protocol.ServiceTaskResponse) error {
	return doVoid(ctx, c, "POST", protocol.WithServiceQuery(urlStr, resp.Service), "", resp, 0)
}

func (c *HTTPPeerClient) FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
	return doJSON[protocol.ServiceBid](ctx, c, "POST", peerID, protocol.PathRel(protocol.PathServicesBid), query)
}

func (c *HTTPPeerClient) AddPeer(peerID string, req protocol.AddPeerRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultRPCTimeout)
	defer cancel()
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathPeersAdd), req, http.StatusOK)
}

func (c *HTTPPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultRPCTimeout)
	defer cancel()
	return doJSON[map[string]protocol.AddressRecord](ctx, c, "POST", sponsorAddres, protocol.PathRel(protocol.PathPeersAnnounce), peerRequest)
}

func (c *HTTPPeerClient) PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error) {
	return doJSON[protocol.RelayRequest](ctx, c, "GET", sponsorAddr, protocol.PathRel(protocol.PathRelayPoll)+"?id="+peerID, nil)
}

func (c *HTTPPeerClient) ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
	return doVoid(ctx, c, "POST", sponsorAddr, protocol.PathRel(protocol.PathRelayReply), resp, http.StatusOK)
}

func (c *HTTPPeerClient) Leave(ctx context.Context, peerID string, leaveReq protocol.PeerIDRequest) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathPeersLeave), leaveReq, http.StatusOK)
}

func (c *HTTPPeerClient) Offline(ctx context.Context, peerID string, offlineReq protocol.PeerIDRequest) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathPeersOffline), offlineReq, http.StatusOK)
}

func (c *HTTPPeerClient) RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error) {
	return doJSON[protocol.ProbeResponse](ctx, c, "POST", targetAddr, protocol.PathRel(protocol.PathPeersProbe), req)
}

func (c *HTTPPeerClient) RotateTLS(ctx context.Context, peerID string, payload protocol.RotateTLSPayload) error {
	return doVoid(ctx, c, "POST", peerID, protocol.PathRel(protocol.PathClusterRotate), payload, http.StatusOK)
}

func (c *HTTPPeerClient) StreamService(ctx context.Context, peerID string, serviceName string, payload map[string]any) (io.ReadCloser, error) {
	bodyReader, contentType, err := prepareBody(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendRequest(ctx, "POST", peerID, protocol.WithServiceQuery(protocol.PathRel(protocol.PathServicesStream), serviceName), bodyReader, contentType)
	if err != nil {
		return nil, err
	}
	return OpenHTTPBody(resp, http.StatusOK)
}
