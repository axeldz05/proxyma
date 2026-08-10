package server_test

import (
	"sync"
	"testing"

	"proxyma/internal/protocol"
	"proxyma/internal/server"

	"github.com/stretchr/testify/require"
)

func TestBandwidthTrackerRecordsAndCategorizes(t *testing.T) {
	t.Parallel()
	bt := server.NewBandwidthTracker()

	bt.RecordBytesSent(1000, protocol.PathUpload)
	bt.RecordBytesReceived(500, protocol.PathDownloadPrefix+"abc123")
	bt.RecordBytesSent(200, protocol.ServicesPrefix+"bid?service=ocr")
	bt.RecordBytesReceived(50, protocol.PathPeersAnnounce)

	sent, recv := bt.GetTotalBandwidth()
	require.Equal(t, int64(1200), sent)
	require.Equal(t, int64(550), recv)

	up, down := bt.GetCurrentBandwidth()
	require.InDelta(t, 1200.0/5.0, up, 0.01)
	require.InDelta(t, 550.0/5.0, down, 0.01)

	svcUp, _ := bt.GetCategoryBandwidth("service:ocr")
	require.InDelta(t, 200.0/5.0, svcUp, 0.01)

	vfsUp, _ := bt.GetCategoryBandwidth("vfs:upload")
	require.InDelta(t, 1000.0/5.0, vfsUp, 0.01)
}

func TestBandwidthCategorizePath(t *testing.T) {
	t.Parallel()
	bt := server.NewBandwidthTracker()

	cases := []struct {
		path string
		want string
	}{
		{protocol.PathUpload, "vfs:upload"},
		{protocol.PathDownloadPrefix + "deadbeef", "vfs:deadbeef"},
		{protocol.PathDownloadPrefix + "deadbeef?x=1", "vfs:deadbeef"},
		{protocol.ServicesPrefix + "submit", "service:submit"},
		{protocol.ServicesPrefix + "bid?service=ocr", "service:ocr"},
		{protocol.ServicesPrefix + "run?name=pipe", "service:pipe"},
		{protocol.PathPeersAnnounce, "other"},
	}
	for _, tc := range cases {
		t.Run(tc.want+"_"+tc.path, func(t *testing.T) {
			require.Equal(t, tc.want, bt.CategorizePath(tc.path))
		})
	}
}

func TestBandwidthTrackerConcurrentRecords(t *testing.T) {
	t.Parallel()
	bt := server.NewBandwidthTracker()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bt.RecordBytesSent(10, protocol.PathUpload)
			bt.RecordBytesReceived(5, protocol.PathDownloadPrefix+"h")
		}()
	}
	wg.Wait()
	sent, recv := bt.GetTotalBandwidth()
	require.Equal(t, int64(500), sent)
	require.Equal(t, int64(250), recv)
}
