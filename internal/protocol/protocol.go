package protocol

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// NewLogger creates the centralized logger for the Proxyma node.
// All entry points (CLI, Android, tests) must use this as the single initializer.
func NewLogger(w io.Writer, debug bool) *slog.Logger {
	var opts slog.HandlerOptions
	if debug {
		opts.Level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &opts))
}

type TaskRequest struct {
	TaskID  string         `json:"task_id"`
	Service string         `json:"service"`
	ReplyTo string         `json:"reply_to"`
	Payload map[string]any `json:"payload"`
}

type DiscoveryQuery struct {
	Service          string   `json:"service"`
	RequiredParams   []string `json:"required_params"`
	SortStrategy     string   `json:"sort_strategy"`
	PayloadSizeBytes int64    `json:"payload_size_bytes"`
}

type ServiceTaskResponse struct {
	TaskID  string         `json:"task_id"`
	Service string         `json:"service"`
	Status  string         `json:"status"`
	Error   string         `json:"error,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
}

type IndexEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Hash    string `json:"hash"`
	Version int    `json:"version"`
	Deleted bool   `json:"deleted"`
}

type CLIFileEntry struct {
	Name       string `json:"name"`
	Version    int    `json:"version"`
	Size       int64  `json:"size"`
	Hash       string `json:"hash"`
	Deleted    bool   `json:"deleted"`
	Subscribed bool   `json:"subscribed"`
	HasLocal   bool   `json:"has_local"`
}

type UnixRequest struct {
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
}

type UnixResponse struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
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
	ID                   string `json:"id"`
	Address              string `json:"address"`
	StoragePath          string `json:"storage_path"`
	Workers              int    `json:"workers"`
	CAPath               string `json:"ca_path"`
	BootstrapNode        string `json:"bootstrap_node,omitempty"`
	STUNServer           string `json:"stun_server,omitempty"`
	IsSponsorOverride    *bool  `json:"is_sponsor_override,omitempty"`
	MinRelayPollInterval int    `json:"min_relay_poll_interval,omitempty"`
	MaxRelayPollInterval int    `json:"max_relay_poll_interval,omitempty"`
	Logger               *slog.Logger
}

const (
	StrategyFastest  = "proxyma/strategy/fastest"
	StrategyCheapest = "proxyma/strategy/cheapest"
	StrategyLowPower = "proxyma/strategy/low_power"
)

type PeerNotification struct {
	File   IndexEntry `json:"file"`
	Source string     `json:"source"`
}

type ServiceNotification struct {
	Action string        `json:"action"` // "add", "remove"
	NodeID string        `json:"node_id"`
	Schema ServiceSchema `json:"schema"`
}

type JoinRequest struct {
	Secret  string `json:"secret"`
	CSR     string `json:"csr"`
	ID      string `json:"id"`
	Address string `json:"address"`
}

type JoinResponse struct {
	Certificate string                   `json:"certificate"`
	CACert      string                   `json:"ca_cert"`
	Peers       map[string]AddressRecord `json:"peers"`
}

type AddressRecord struct {
	Addresses []string `json:"addresses"`
	Sequence  int64    `json:"sequence"`
	IsSponsor bool     `json:"is_sponsor"`
}

type AddPeerRequest struct {
	ID      string        `json:"id"`
	Address AddressRecord `json:"address"`
}

func SaveConfig(cfg NodeConfig) error {
	configPath := filepath.Join(cfg.StoragePath, "config.json")
	file, err := os.Create(configPath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(cfg)
}

func LoadConfig(storagePath string) (NodeConfig, error) {
	configPath := filepath.Join(storagePath, "config.json")
	file, err := os.Open(configPath)
	if err != nil {
		return NodeConfig{}, err
	}
	defer func() { _ = file.Close() }()

	var cfg NodeConfig
	err = json.NewDecoder(file).Decode(&cfg)
	return cfg, err
}


// RelayRequest encapsulates an HTTP request to be forwarded by the Sponsor
type RelayRequest struct {
	ReqID   string            `json:"req_id"`
	Target  string            `json:"target"` // PeerID
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// RelayResponse encapsulates an HTTP response returned by the target node
type RelayResponse struct {
	ReqID      string            `json:"req_id"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
}

type ProbeRequest struct {
	Address string `json:"address"`
}

type ProbeResponse struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}
