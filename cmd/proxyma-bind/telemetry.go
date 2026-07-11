package proxyma_bind

import (
	"encoding/json"
	"fmt"

	"proxyma/internal/protocol"
)

// GetBandwidthStatsJson returns real-time bandwidth statistics.
func GetBandwidthStatsJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "bandwidth", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	stats := s.LocalBandwidthStats()
	b, _ := json.Marshal(stats)
	return string(b)
}

// GetLogsJson returns JSON logs.
func GetLogsJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "logs", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	protocol.LogBufferMu.Lock()
	defer protocol.LogBufferMu.Unlock()
	if protocol.LogBuffer == nil {
		return "[]"
	}
	b, _ := json.Marshal(protocol.LogBuffer)
	return string(b)
}
