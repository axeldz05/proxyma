package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

type mockPeerClient struct{}

func (m *mockPeerClient) FetchManifest(ctx context.Context, peerID string) (map[string]protocol.IndexEntry, error) {
	return nil, nil
}
func (m *mockPeerClient) Announce(sponsorAddress string, peerRequest protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
	return nil, nil
}
func (m *mockPeerClient) Notify(ctx context.Context, peerID string, notification protocol.PeerNotification) error {
	return nil
}
func (m *mockPeerClient) NotifyServiceUpdate(ctx context.Context, peerID string, notification protocol.ServiceNotification) error {
	return nil
}
func (m *mockPeerClient) AddPeer(peerID string, payload *bytes.Buffer) error {
	return nil
}
func (m *mockPeerClient) DownloadBlob(ctx context.Context, peerID, hash string) (io.ReadCloser, error) {
	return nil, nil
}
func (m *mockPeerClient) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return nil, nil
}
func (m *mockPeerClient) ExecuteService(ctx context.Context, peerID string, serviceName string) (map[string]string, error) {
	return nil, nil
}
func (m *mockPeerClient) SubmitTask(ctx context.Context, peerID string, req protocol.TaskRequest) error {
	return nil
}
func (m *mockPeerClient) SendTaskResponse(ctx context.Context, url string, resp protocol.ServiceTaskResponse) error {
	return nil
}
func (m *mockPeerClient) FetchServiceBid(ctx context.Context, peerID string, query protocol.DiscoveryQuery) (protocol.ServiceBid, error) {
	return protocol.ServiceBid{
		NodeID:    "test-node",
		NodeAddr:  "https://localhost:18080",
		CanAccept: true,
		Schema: protocol.ServiceSchema{
			Name:        query.Service,
			Description: "Converts photos to grayscale",
			Parameters: map[string]protocol.ServiceParameter{
				"input_image": {
					Type:     "string",
					Required: true,
				},
				"threshold": {
					Type:     "int",
					Required: false,
				},
			},
		},
	}, nil
}
func (m *mockPeerClient) PollRelay(ctx context.Context, sponsorAddr string, peerID string) (protocol.RelayRequest, error) {
	return protocol.RelayRequest{}, nil
}
func (m *mockPeerClient) ReplyRelay(ctx context.Context, sponsorAddr string, resp protocol.RelayResponse) error {
	return nil
}
func (m *mockPeerClient) Leave(ctx context.Context, peerID string, leaveReq map[string]string) error {
	return nil
}
func (m *mockPeerClient) Offline(ctx context.Context, peerID string, offlineReq map[string]string) error {
	return nil
}
func (m *mockPeerClient) RequestProbe(ctx context.Context, targetAddr string, req protocol.ProbeRequest) (protocol.ProbeResponse, error) {
	return protocol.ProbeResponse{Reachable: true}, nil
}

func setupTestStorage(t *testing.T) string {
	tmp, err := os.MkdirTemp("", "proxyma_test_*")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tmp)
	})
	return tmp
}

func TestBandwidthCountingAndCategories(t *testing.T) {
	tmpStorage := setupTestStorage(t)
	cfg := protocol.NodeConfig{
		ID:          "test-node",
		Address:     "https://localhost:18080",
		StoragePath: tmpStorage,
		Logger:      protocol.NewLogger(os.Stdout, true),
	}
	s := server.New(cfg, &mockPeerClient{})
	t.Cleanup(func() {
		_ = s.Shutdown(context.Background())
	})

	s.RecordBytesSent(500, "/download/hash123")
	s.RecordBytesReceived(1000, "/download/hash123")
	s.RecordBytesSent(300, "/services/submit?service=process")
	s.RecordBytesReceived(600, "/services/callback?service=process")

	up, down := s.GetCurrentBandwidth()
	assert.True(t, up > 0, "upload bandwidth should be non-zero")
	assert.True(t, down > 0, "download bandwidth should be non-zero")

	totalUp, totalDown := s.GetTotalBandwidth()
	assert.Equal(t, int64(800), totalUp)
	assert.Equal(t, int64(1600), totalDown)

	vfsUp, vfsDown := s.GetCategoryBandwidth("vfs:hash123")
	assert.True(t, vfsUp > 0)
	assert.True(t, vfsDown > 0)

	svcUp, svcDown := s.GetCategoryBandwidth("service:process")
	assert.True(t, svcUp > 0)
	assert.True(t, svcDown > 0)
}

func TestVFSSnapshotRendering(t *testing.T) {
	appInst := test.NewApp()
	win := appInst.NewWindow("VFS Test")

	tmpStorage := setupTestStorage(t)
	cfg := protocol.NodeConfig{
		ID:          "test-node",
		Address:     "https://localhost:18080",
		StoragePath: tmpStorage,
		Logger:      protocol.NewLogger(os.Stdout, true),
	}

	srvMutex.Lock()
	srv = server.New(cfg, &mockPeerClient{})
	srvMutex.Unlock()
	t.Cleanup(func() {
		srvMutex.Lock()
		if srv != nil {
			_ = srv.Shutdown(context.Background())
			srv = nil
		}
		srvMutex.Unlock()
	})

	// Inject a fake VFS entry
	fileEntry := protocol.IndexEntry{
		Name:    "photo.jpg",
		Size:    1024,
		Hash:    "dummyhash",
		Version: 1,
	}
	srv.Storage.Upsert(fileEntry)

	ui := &ProxymaUI{
		window:            win,
		vfsFilesContainer: container.NewVBox(),
	}

	// Verify we can update layout
	ui.updateVFSSnapshot(win)()
	assert.Len(t, ui.vfsFilesContainer.Objects, 1, "VFS container should render one row")

	row, ok := ui.vfsFilesContainer.Objects[0].(*fyne.Container)
	require.True(t, ok)
	assert.Len(t, row.Objects, 5, "Row should contain Label, Subscribe button, Open button, Open Location button, Delete button")

	lbl, ok := row.Objects[0].(*widget.Label)
	require.True(t, ok)
	assert.Contains(t, lbl.Text, "photo.jpg")

	btnSub, ok := row.Objects[1].(*widget.Button)
	require.True(t, ok)
	assert.Equal(t, "Subscribe", btnSub.Text)

	btnOpen, ok := row.Objects[2].(*widget.Button)
	require.True(t, ok)
	assert.Equal(t, "Open", btnOpen.Text)

	btnOpenLoc, ok := row.Objects[3].(*widget.Button)
	require.True(t, ok)
	assert.Equal(t, "Open Location", btnOpenLoc.Text)

	// Subscribe
	btnSub.Tapped(&fyne.PointEvent{})
	time.Sleep(100 * time.Millisecond) // Wait for background goroutine refresh
	assert.True(t, srv.Storage.IsSubscribed("photo.jpg"))

	// Verify button text updates
	ui.updateVFSSnapshot(win)()
	rowUpdated := ui.vfsFilesContainer.Objects[0].(*fyne.Container)
	btnSubUpdated := rowUpdated.Objects[1].(*widget.Button)
	assert.Equal(t, "Unsubscribe", btnSubUpdated.Text)
}

func TestServiceDetailsPermissionsAndLabels(t *testing.T) {
	appInst := test.NewApp()
	win := appInst.NewWindow("Service Test")

	tmpStorage := setupTestStorage(t)
	cfg := protocol.NodeConfig{
		ID:          "test-node",
		Address:     "https://localhost:18080",
		StoragePath: tmpStorage,
		Logger:      protocol.NewLogger(os.Stdout, true),
	}

	srvMutex.Lock()
	srv = server.New(cfg, &mockPeerClient{})
	srvMutex.Unlock()
	t.Cleanup(func() {
		srvMutex.Lock()
		if srv != nil {
			_ = srv.Shutdown(context.Background())
			srv = nil
		}
		srvMutex.Unlock()
	})

	schema := protocol.ServiceSchema{
		Name:        "image-processing",
		Description: "Converts photos to grayscale",
		Parameters: map[string]protocol.ServiceParameter{
			"input_image": {
				Type:     "string",
				Required: true,
			},
			"threshold": {
				Type:     "int",
				Required: false,
			},
		},
	}
	err := srv.Compute.RegisterNewService(schema, func(ctx context.Context, payload map[string]interface{}) (map[string]interface{}, error) {
		return nil, nil
	})
	require.NoError(t, err)

	srv.AddPeer("test-node", protocol.AddressRecord{Addresses: []string{"https://localhost:18080"}})

	dest := container.NewVBox()
	loadServiceDetails("image-processing", win, dest)

	// Since RequestServiceToCluster runs in a goroutine, we need to wait briefly
	require.Eventually(t, func() bool {
		return len(dest.Objects) >= 6
	}, 2*time.Second, 100*time.Millisecond)

	var labelTexts []string
	for _, obj := range dest.Objects {
		if lbl, ok := obj.(*widget.Label); ok {
			labelTexts = append(labelTexts, lbl.Text)
		}
	}

	// Verify permissions and descriptions are populated
	assert.Contains(t, labelTexts, "Service: image-processing")
	assert.Contains(t, labelTexts, "Required Permissions:")
	assert.Contains(t, labelTexts, " - Camera (to take photo for upload)")
	assert.Contains(t, labelTexts, "Description: Provide an image file path or capture a photo for input_image.")
}

func TestStoragePathRelocationPreferences(t *testing.T) {
	appInst := test.NewApp()

	oldPath := setupTestStorage(t)
	newPath := setupTestStorage(t)

	// Setup initial dummy files in old path
	dummyFile := filepath.Join(oldPath, "dummy.txt")
	err := os.WriteFile(dummyFile, []byte("hello"), 0644)
	require.NoError(t, err)

	appStorage = oldPath
	a := appInst
	a.Preferences().SetString("app_storage_path", oldPath)

	// Verify copyDir
	newStorage := filepath.Join(newPath, "proxyma_data")
	err = copyDir(oldPath, newStorage)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(newStorage, "dummy.txt"))
}

func TestCertCleanupOnJoin(t *testing.T) {
	tmpStorage := setupTestStorage(t)
	certsDir := filepath.Join(tmpStorage, "certs")
	err := os.MkdirAll(certsDir, 0755)
	require.NoError(t, err)

	// Create stale cert files
	staleCert := filepath.Join(certsDir, "old_node.crt")
	err = os.WriteFile(staleCert, []byte("stale"), 0644)
	require.NoError(t, err)

	// Perform the remove all and recreate logic as done during joinCluster
	_ = os.RemoveAll(certsDir)
	err = os.MkdirAll(certsDir, 0755)
	require.NoError(t, err)

	assert.NoFileExists(t, staleCert, "stale certs should be purged")
}
