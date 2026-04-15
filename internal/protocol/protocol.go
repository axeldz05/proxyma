package protocol

import "log/slog"

type TaskRequest struct {
	TaskID  string         `json:"task_id"`
	Service string         `json:"service"`
	ReplyTo string         `json:"reply_to"` 
	Payload  map[string]any `json:"payload"`
}

type DiscoveryQuery struct {
	Service          string   `json:"service"`
	RequiredParams   []string `json:"required_params"`
	SortStrategy     string   `json:"sort_strategy"`
	PayloadSizeBytes int64    `json:"payload_size_bytes"`
}

type ServiceTaskResponse struct {
	TaskID    string         `json:"task_id"`
	Service   string         `json:"service"`
	Status    string         `json:"status"`
	Error     string         `json:"error,omitempty"`
	Outputs   map[string]any `json:"outputs,omitempty"`
}

type IndexEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Hash    string `json:"hash"`
	Version int    `json:"version"`
	Deleted bool   `json:"deleted"`
}

type ServiceSchema struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Parameters  map[string]ServiceParameter `json:"parameters"`
}

type ServiceParameter struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type ServiceBid struct {
	NodeID          string        `json:"node_id"`
	NodeAddr        string        `json:"node_addr"`
	Schema          ServiceSchema `json:"schema"`
	EstimatedMillis int64         `json:"estimated_millis"` 
	CanAccept       bool          `json:"can_accept"`
}

type NodeConfig struct {
	ID          string
	Address     string
	StoragePath string
	Workers     int
	Logger		*slog.Logger
}

const (
	StrategyFastest    = "proxyma/strategy/fastest"
	StrategyCheapest   = "proxyma/strategy/cheapest"
	StrategyLowPower   = "proxyma/strategy/low_power"
)

type PeerNotification struct {
	File   IndexEntry `json:"file"`
	Source string     `json:"source"`
}

