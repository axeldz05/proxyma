package testutil

import (
	"bytes"
	"context"
	"io"
	"proxyma/internal/protocol"
)

type MockPeerClient struct {
	OnFetchManifest    func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error)
	OnAnnounce		   func(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]string, error)
	OnNotify           func(ctx context.Context, addr string, n protocol.PeerNotification) error
	OnAddPeer          func(addr string, payload *bytes.Buffer) error
	OnDownloadBlob     func(ctx context.Context, addr, hash string) (io.ReadCloser, error)
	OnDiscoverServices func(ctx context.Context, addr string) ([]string, error)
	OnSubmitTask       func(ctx context.Context, addr string, req protocol.TaskRequest) error
	OnFetchServiceBid  func(ctx context.Context, addr string, q protocol.DiscoveryQuery) (protocol.ServiceBid, error)
	OnSendTaskResponse func(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error
}

func (m *MockPeerClient) AddPeer(addr string, payload *bytes.Buffer) error {
	return nil
}

func (m *MockPeerClient) Announce(sponsorAddres string, peerRequest protocol.AddPeerRequest) (map[string]string, error) {
	return map[string]string{}, nil
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
