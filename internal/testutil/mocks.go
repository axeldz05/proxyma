package testutil

import (
	"bytes"
	"context"
	"io"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
)

type MockPeerClient struct {
	OnFetchManifest       func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error)
	OnAnnounce            func(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error)
	OnNotify              func(ctx context.Context, addr string, n protocol.PeerNotification) error
	OnNotifyServiceUpdate func(ctx context.Context, addr string, n protocol.ServiceNotification) error
	OnAddPeer             func(addr string, payload *bytes.Buffer) error
	OnDownloadBlob        func(ctx context.Context, addr, hash string) (io.ReadCloser, error)
	OnDiscoverServices    func(ctx context.Context, addr string) ([]string, error)
	OnSubmitTask          func(ctx context.Context, addr string, req protocol.TaskRequest) error
	OnFetchServiceBid     func(ctx context.Context, addr string, q protocol.DiscoveryQuery) (protocol.ServiceBid, error)
	OnSendTaskResponse    func(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error
	OnPollRelay           func(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error)
	OnReplyRelay          func(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error
	OnLeave               func(ctx context.Context, peerID string, leaveReq map[string]string) error
	OnOffline             func(ctx context.Context, peerID string, offlineReq map[string]string) error
	OnRequestProbe        func(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error)
	OnRotateTLS           func(ctx context.Context, peerID string, payload map[string]string) error
	OnNotifyPipelineSchema func(ctx context.Context, addr string, n protocol.PipelineNotification) error
}

func (m *MockPeerClient) AddPeer(addr string, payload *bytes.Buffer) error {
	return nil
}

func (m *MockPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
	if m.OnAnnounce != nil {
		return m.OnAnnounce(sponsorAddres, peerRequest)
	}
	return map[string]protocol.AddressRecord{}, nil
}

func (m *MockPeerClient) FetchManifest(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
	if m.OnFetchManifest != nil {
		return m.OnFetchManifest(ctx, addr)
	}
	return nil, nil
}

func (m *MockPeerClient) Notify(ctx context.Context, addr string, n protocol.PeerNotification) error {
	if m.OnNotify != nil {
		return m.OnNotify(ctx, addr, n)
	}
	return nil
}

func (m *MockPeerClient) NotifyServiceUpdate(ctx context.Context, addr string, n protocol.ServiceNotification) error {
	if m.OnNotifyServiceUpdate != nil {
		return m.OnNotifyServiceUpdate(ctx, addr, n)
	}
	return nil
}

func (m *MockPeerClient) DownloadBlob(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
	if m.OnDownloadBlob != nil {
		return m.OnDownloadBlob(ctx, addr, hash)
	}
	return nil, nil
}

func (m *MockPeerClient) DiscoverServices(ctx context.Context, addr string) ([]string, error) {
	if m.OnDiscoverServices != nil {
		return m.OnDiscoverServices(ctx, addr)
	}
	return nil, nil
}

func (m *MockPeerClient) SubmitTask(ctx context.Context, addr string, req protocol.TaskRequest) error {
	if m.OnSubmitTask != nil {
		return m.OnSubmitTask(ctx, addr, req)
	}
	return nil
}

func (m *MockPeerClient) FetchServiceBid(ctx context.Context, addr string, q protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
	if m.OnFetchServiceBid != nil {
		return m.OnFetchServiceBid(ctx, addr, q)
	}
	return protocol.ServiceBid{}, nil
}

func (m *MockPeerClient) SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error {
	if m.OnSendTaskResponse != nil {
		return m.OnSendTaskResponse(ctx, url, resp)
	}
	return nil
}

func (m *MockPeerClient) ExecuteService(ctx context.Context, addr string, svc string) (map[string]string, error) {
	return nil, nil
}

func (m *MockPeerClient) PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error) {
	if m.OnPollRelay != nil {
		return m.OnPollRelay(ctx, sponsorAddr, peerID)
	}
	return protocol.RelayRequest{}, nil
}

func (m *MockPeerClient) ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
	if m.OnReplyRelay != nil {
		return m.OnReplyRelay(ctx, sponsorAddr, resp)
	}
	return nil
}

func (m *MockPeerClient) Leave(ctx context.Context, peerID string, leaveReq map[string]string) error {
	if m.OnLeave != nil {
		return m.OnLeave(ctx, peerID, leaveReq)
	}
	return nil
}

func (m *MockPeerClient) Offline(ctx context.Context, peerID string, offlineReq map[string]string) error {
	if m.OnOffline != nil {
		return m.OnOffline(ctx, peerID, offlineReq)
	}
	return nil
}

func (m *MockPeerClient) RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error) {
	if m.OnRequestProbe != nil {
		return m.OnRequestProbe(ctx, targetAddr, req)
	}
	return protocol.ProbeResponse{Reachable: true}, nil
}

func (m *MockPeerClient) RotateTLS(ctx context.Context, peerID string, payload map[string]string) error {
	if m.OnRotateTLS != nil {
		return m.OnRotateTLS(ctx, peerID, payload)
	}
	return nil
}

func (m *MockPeerClient) NotifyPipelineSchema(ctx context.Context, addr string, n protocol.PipelineNotification) error {
	if m.OnNotifyPipelineSchema != nil {
		return m.OnNotifyPipelineSchema(ctx, addr, n)
	}
	return nil
}

func (m *MockPeerClient) StreamService(ctx context.Context, peerID string, serviceName string, payload map[string]any) (io.ReadCloser, error) {
	return nil, nil
}

func (m *MockPeerClient) UpdatePeerRoute(peerID string, record protocol.AddressRecord) {}
func (m *MockPeerClient) RemovePeerRoute(peerID string)                                 {}
func (m *MockPeerClient) SetNodeID(id string)                                           {}
func (m *MockPeerClient) SetOwnAddress(addr string)                                     {}
func (m *MockPeerClient) UpdateSponsorAddress(addr string)                              {}
func (m *MockPeerClient) CloseIdleConnections()                                         {}
func (m *MockPeerClient) SetQUICManager(qm *p2p.QUICManager)                             {}

