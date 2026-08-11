package protocol

import "time"

// Shared RPC / task / dial timeouts (SSOT). server.PeerRPC* aliases domain-specific peer policy on top.
const (
	RPCTimeoutSync         = 10 * time.Second // short peer RPC / DefaultRPCTimeout
	RPCTimeoutTaskWait     = 90 * time.Second // wait for task completion
	RPCTimeoutTaskCallback = 5 * time.Second  // SendTaskResponse callback

	DialTimeoutJoin       = 3 * time.Second  // cluster join HTTP client
	DialTimeoutRouteProbe = 3 * time.Second  // TCP reachability before direct route (≠ PeerRPCProbe)
	HolePunchAttempt      = 8 * time.Second  // outer hole-punch budget (≥ HolePunchWait)
	HolePunchWait         = 8 * time.Second  // inner punch wait; PeerRPCQUICWait aliases this
	PrewarmHolePunch      = 25 * time.Second // pre-warm InitiateHolePunch context

	HandlerDialUnary  = 10 * time.Second // local unary gRPC/HTTP handler client
	HandlerDialStream = 30 * time.Second // local stream/WebRTC/screen handler client
)
