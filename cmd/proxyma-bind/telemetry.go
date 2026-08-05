package proxyma_bind

import (
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
		return s.LocalLogs(), nil
	})
}
