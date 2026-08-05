package proxyma_bind

import (
	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

// GetBandwidthStatsJson returns real-time bandwidth statistics.
func GetBandwidthStatsJson() string {
	return dispatchUnixOrLocal("bandwidth", nil, func(s *server.Server) (any, error) {
		return s.LocalBandwidthStats(), nil
	})
}

// GetLogsJson returns JSON logs.
func GetLogsJson() string {
	return dispatchUnixOrLocal("logs", nil, func(s *server.Server) (any, error) {
		protocol.LogBufferMu.RLock()
		defer protocol.LogBufferMu.RUnlock()
		if protocol.LogBuffer == nil {
			return []protocol.LogRecord{}, nil
		}
		out := make([]protocol.LogRecord, len(protocol.LogBuffer))
		copy(out, protocol.LogBuffer)
		return out, nil
	})
}
