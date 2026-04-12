package main

import (
	"net/http/httptest"
	"proxyma/storage"
	"sync"
	"log/slog"
)

type IndexEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Hash    string `json:"hash"`
	Version int    `json:"version"`
	Deleted bool   `json:"deleted"`
}

type PeerNotification struct {
	File   IndexEntry `json:"file"`
	Source string     `json:"source"`
}

type DownloadJob struct {
	File   IndexEntry
	Source string
}

type Server struct {
	config			NodeConfig
	peerClient  	PeerClient
	Peers   		map[string]string
	storage 		storage.Storage
	vfs 			*VFS
	downloadQueue 	chan DownloadJob
	server 			*httptest.Server
	// TODO: try using BoltDB / Badger
	subscriptions   *sync.Map
	serviceRegistry *ServiceRegistry
}

type NodeConfig struct {
	ID          string
	Address     string
	StoragePath string
	Workers     int
	Logger		*slog.Logger
	DebugLogger *slog.Logger
}

type TaskRequest struct {
	TaskID  string         `json:"task_id"`
	Service string         `json:"service"`
	ReplyTo string         `json:"reply_to"` 
	Payload  map[string]any `json:"payload"`
}

type ServiceParameter struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type ServiceSchema struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Parameters  map[string]ServiceParameter `json:"parameters"`
}

type ServiceRegistry struct {
	mu		sync.RWMutex
	schemas map[string]ServiceSchema
}

const (
	StrategyFastest    = "proxyma/strategy/fastest"
	StrategyCheapest   = "proxyma/strategy/cheapest"
	StrategyLowPower   = "proxyma/strategy/low_power"
)

type DiscoveryQuery struct {
	Service          string   `json:"service"`
	RequiredParams   []string `json:"required_params"`
	SortStrategy     string   `json:"sort_strategy"`
	PayloadSizeBytes int64    `json:"payload_size_bytes"`
}

type ServiceBid struct {
	NodeID          string        `json:"node_id"`
	NodeAddr        string        `json:"node_addr"`
	Schema          ServiceSchema `json:"schema"`
	EstimatedMillis int64         `json:"estimated_millis"` 
	CanAccept       bool          `json:"can_accept"`
}

