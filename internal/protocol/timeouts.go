package protocol

import "time"

// Shared RPC / task timeouts (SSOT). server.PeerRPC* aliases domain-specific policy on top.
const (
	RPCTimeoutSync         = 10 * time.Second // short peer RPC / DefaultRPCTimeout
	RPCTimeoutTaskWait     = 90 * time.Second // wait for task completion
	RPCTimeoutTaskCallback = 5 * time.Second  // SendTaskResponse callback
)
