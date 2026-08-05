package server

import (
	"strings"
	"sync"
	"time"

	"proxyma/internal/protocol"
)

// TransferRecord stores telemetry data for byte transfer of a specific category at a point in time.
type TransferRecord struct {
	Timestamp time.Time
	Bytes     int64
	Category  string
}

// BandwidthTracker keeps track of bytes sent and received, with categorization and history.
type BandwidthTracker struct {
	totalSent       int64
	totalReceived   int64
	sentHistory     []TransferRecord
	receivedHistory []TransferRecord
	mu              sync.RWMutex
}

// NewBandwidthTracker creates a new BandwidthTracker.
func NewBandwidthTracker() *BandwidthTracker {
	return &BandwidthTracker{}
}

func (bt *BandwidthTracker) recordTransfer(isSent bool, n int64, path string) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	rec := TransferRecord{
		Timestamp: time.Now(),
		Bytes:     n,
		Category:  bt.CategorizePath(path),
	}
	threshold := rec.Timestamp.Add(-5 * time.Second)

	if isSent {
		bt.totalSent += n
		bt.sentHistory, _ = pruneHistory(append(bt.sentHistory, rec), threshold)
	} else {
		bt.totalReceived += n
		bt.receivedHistory, _ = pruneHistory(append(bt.receivedHistory, rec), threshold)
	}
}

// RecordBytesSent records the number of bytes sent via a request path.
func (bt *BandwidthTracker) RecordBytesSent(n int64, path string) {
	bt.recordTransfer(true, n, path)
}

// RecordBytesReceived records the number of bytes received via a request path.
func (bt *BandwidthTracker) RecordBytesReceived(n int64, path string) {
	bt.recordTransfer(false, n, path)
}

// GetCurrentBandwidth calculates the upload and download bandwidth for the last 5 seconds.
func (bt *BandwidthTracker) GetCurrentBandwidth() (float64, float64) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	threshold := time.Now().Add(-5 * time.Second)
	var sentSum, recvSum int64
	bt.sentHistory, sentSum = pruneHistory(bt.sentHistory, threshold)
	bt.receivedHistory, recvSum = pruneHistory(bt.receivedHistory, threshold)

	return float64(sentSum) / 5.0, float64(recvSum) / 5.0
}

// GetCategoryBandwidth returns the upload/download bandwidth for a specific category in the last 5 seconds.
func (bt *BandwidthTracker) GetCategoryBandwidth(category string) (float64, float64) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	threshold := time.Now().Add(-5 * time.Second)
	sentSum := sumCategory(bt.sentHistory, threshold, category)
	recvSum := sumCategory(bt.receivedHistory, threshold, category)
	return float64(sentSum) / 5.0, float64(recvSum) / 5.0
}

func pruneHistory(history []TransferRecord, threshold time.Time) ([]TransferRecord, int64) {
	var total int64
	var i int
	for i = len(history) - 1; i >= 0; i-- {
		rec := history[i]
		if rec.Timestamp.Before(threshold) {
			break
		}
		total += rec.Bytes
	}
	if i >= 0 {
		return history[i+1:], total
	}
	return history, total
}

func sumCategory(history []TransferRecord, threshold time.Time, category string) int64 {
	var total int64
	for _, rec := range history {
		if rec.Timestamp.After(threshold) && rec.Category == category {
			total += rec.Bytes
		}
	}
	return total
}

// GetTotalBandwidth returns the total bytes sent and received.
func (bt *BandwidthTracker) GetTotalBandwidth() (int64, int64) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.totalSent, bt.totalReceived
}

// CategorizePath maps a request path to a telemetry/bandwidth category string.
func (bt *BandwidthTracker) CategorizePath(path string) string {
	// should consider making a more generalized form of categorizing
	// without needing to hardcode prefixes
	if strings.HasPrefix(path, protocol.PathDownloadPrefix) {
		cleanPath := path
		if idx := strings.Index(path, "?"); idx != -1 {
			cleanPath = path[:idx]
		}
		parts := strings.Split(cleanPath, "/")
		if len(parts) >= 3 {
			hash := parts[2]
			return "vfs:" + hash
		}
		return "vfs:download"
	}
	if strings.HasPrefix(path, protocol.PathUpload) {
		return "vfs:upload"
	}
	if strings.HasPrefix(path, protocol.ServicesPrefix) {
		cleanPath := strings.TrimPrefix(path, protocol.ServicesPrefix)
		if idx := strings.Index(cleanPath, "?"); idx != -1 {
			queryParams := cleanPath[idx+1:]
			basePath := cleanPath[:idx]
			for _, param := range strings.Split(queryParams, "&") {
				parts := strings.SplitN(param, "=", 2)
				if len(parts) == 2 && (parts[0] == "service" || parts[0] == "name") {
					return "service:" + parts[1]
				}
			}
			return "service:" + basePath
		}
		return "service:" + cleanPath
	}
	return "other"
}
