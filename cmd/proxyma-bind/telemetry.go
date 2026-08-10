package proxyma_bind

// GetBandwidthStatsJson returns real-time bandwidth statistics.
func GetBandwidthStatsJson() string {
	return InvokeDomainAction("telemetry", "stats", nil)
}

// GetLogsJson returns JSON logs.
func GetLogsJson() string {
	return InvokeDomainAction("telemetry", "logs", nil)
}
