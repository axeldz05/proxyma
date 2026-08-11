package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	TaskID          string         `json:"task_id"`
	Service         string         `json:"service"`
	RequesterNodeID string         `json:"requester_node_id"`
	ReplyTo         string         `json:"reply_to"`
	Payload         map[string]any `json:"payload"`
}

type PipelineConnection struct {
	FromStep string `json:"from_step"`
	FromPort string `json:"from_port"`
	ToStep   string `json:"to_step"`
	ToPort   string `json:"to_port"`
}

type PipelineStep struct {
	ID           string `json:"id"`
	Service      string `json:"service"`
	TargetNodeID string `json:"target_node_id,omitempty"`
}

type PipelineSchema struct {
	ID          string               `json:"id"`
	Version     int                  `json:"version"`
	Steps       []PipelineStep       `json:"steps"`
	Connections []PipelineConnection `json:"connections,omitempty"`
}

const (
	ActionAdd    = "add"
	ActionRemove = "remove"
	ActionModify = "modify"
)

// DefaultTCPPort is the SSOT fallback listen/advertise TCP port when Config.Address has none.
const DefaultTCPPort = "8080"

// DefaultInviteMinutes is the SSOT default invite TTL.
const DefaultInviteMinutes = 15

// SockFileName is the daemon unix socket basename under StoragePath (SSOT).
const SockFileName = "proxyma.sock"

// UnixSockPath returns the absolute path to the daemon unix socket.
func UnixSockPath(storagePath string) string {
	return filepath.Join(storagePath, SockFileName)
}

type PipelineNotification struct {
	Schema PipelineSchema `json:"schema"`
	Action string         `json:"action"` // ActionAdd / ActionRemove
}

type PipelineContext struct {
	Steps       []PipelineStep            `json:"steps"`
	CurrentStep int                       `json:"current_step"`
	Outputs     map[string]map[string]any `json:"outputs"`
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

type VFSFileStatus struct {
	Name       string  `json:"name"`
	Version    int     `json:"version"`
	Size       int64   `json:"size"`
	Hash       string  `json:"hash"`
	Subscribed bool    `json:"subscribed"`
	HasLocal   bool    `json:"hasLocal"`
	Deleted    bool    `json:"deleted"`
	UpSpeed    float64 `json:"upSpeed"`
	DownSpeed  float64 `json:"downSpeed"`
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

type ServiceUIConfig struct {
	Type       string `json:"type,omitempty"`        // "declarative", "web_app", "custom_layout"
	VFSPath    string `json:"vfs_path,omitempty"`    // VFS path to HTML assets
	LocalPath  string `json:"local_path,omitempty"`  // Local path to asset folder or file
	URL        string `json:"url,omitempty"`         // HTTP server URL if self-hosted by service
	WidgetType string `json:"widget_type,omitempty"` // "rich_text", "graph_editor", "stream_player"
}

type ServiceSchema struct {
	Name        string                      `json:"name"`
	Type        ServiceType                 `json:"type,omitempty"`
	Description string                      `json:"description"`
	Parameters  map[string]ServiceParameter `json:"parameters"`
	Outputs     map[string]ServiceParameter `json:"outputs,omitempty"`
	UI          *ServiceUIConfig            `json:"ui,omitempty"`
}

func (s ServiceSchema) IsStreaming() bool {
	return s.Type.IsStreaming()
}

// NormalizeServiceSchema fills Name/Type from map key / local service type and normalizes Type (L1 SSOT).
func NormalizeServiceSchema(name string, schema ServiceSchema, serviceType ServiceType) ServiceSchema {
	if schema.Name == "" {
		schema.Name = name
	}
	if schema.Type == "" {
		schema.Type = serviceType
	}
	schema.Type = schema.Type.Normalize()
	return schema
}

type ServiceParameter struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	UIHint   string   `json:"ui_hint,omitempty"` // e.g. "image_picker", "file_picker"
}

const (
	ParamTypeString = "string"
	ParamTypeFile   = "file"
	ParamTypeBool   = "bool"
	ParamTypeInt    = "int"
	ParamTypeFloat  = "float"
)

// UI hint constants (SSOT).
const (
	UIHintFilePicker  = "file_picker"
	UIHintImagePicker = "image_picker"
	UIHintAudioPicker = "audio_picker"
)

// InferUIHint returns a UI hint for a parameter from its name and type (L1 SSOT).
// Empty string means no special picker. Prefer explicit UIHint on the schema when set.
func InferUIHint(paramName, paramType string) string {
	if paramType != ParamTypeFile {
		return ""
	}
	lower := strings.ToLower(paramName)
	if strings.Contains(lower, "image") || strings.Contains(lower, "img") || strings.Contains(lower, "photo") {
		return UIHintImagePicker
	}
	return UIHintFilePicker
}

// EffectiveUIHint returns explicit hint if set, otherwise InferUIHint.
func EffectiveUIHint(paramName string, p ServiceParameter) string {
	if p.UIHint != "" {
		return p.UIHint
	}
	return InferUIHint(paramName, p.Type)
}

// IsFilePickerHint reports whether hint is any file-like picker (L2).
func IsFilePickerHint(hint string) bool {
	return hint == UIHintFilePicker || hint == UIHintImagePicker || hint == UIHintAudioPicker
}

// IsImagePickerHint reports whether hint is an image picker (L2).
func IsImagePickerHint(hint string) bool {
	return hint == UIHintImagePicker
}

// MissingRequired returns names of required parameters absent or empty in payload (L1).
func MissingRequired(schema ServiceSchema, payload map[string]any) []string {
	var missing []string
	for name, p := range schema.Parameters {
		if !p.Required {
			continue
		}
		v, ok := payload[name]
		if !ok || v == nil {
			missing = append(missing, name)
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// CoerceDefault parses p.Default into a typed value (L1). Falls back to type samples when empty.
func (p ServiceParameter) CoerceDefault(paramName string) any {
	if p.Default != "" {
		switch p.Type {
		case ParamTypeBool:
			return ParseDefaultBool(p.Default)
		case ParamTypeInt:
			return ParseDefaultInt(p.Default)
		case ParamTypeFloat:
			var val float64
			_, _ = fmt.Sscanf(p.Default, "%f", &val)
			return val
		default:
			return p.Default
		}
	}
	if len(p.Options) > 0 {
		return p.Options[0]
	}
	switch p.Type {
	case ParamTypeBool:
		return true
	case ParamTypeInt:
		return 100
	case ParamTypeFloat:
		return 1.0
	case ParamTypeFile:
		if EffectiveUIHint(paramName, p) == UIHintImagePicker {
			return "/path/to/image.jpg"
		}
		return "/path/to/input_file"
	default:
		return "example_value"
	}
}

// ParseDefaultBool returns a bool default for CLI flag registration.
func ParseDefaultBool(defaultValue string) bool {
	return defaultValue == "true" || defaultValue == "1"
}

// ParseDefaultInt returns an int default for CLI flag registration.
func ParseDefaultInt(defaultValue string) int {
	var val int
	if defaultValue != "" {
		_, _ = fmt.Sscanf(defaultValue, "%d", &val)
	}
	return val
}

// ValidateValue checks that value matches the expected schema type and Options (L1).
func (p ServiceParameter) ValidateValue(paramName string, value any) error {
	switch p.Type {
	case ParamTypeString, ParamTypeFile:
		if _, ok := value.(string); !ok {
			return ParamTypeError(paramName, "string")
		}
	case ParamTypeBool:
		if _, ok := value.(bool); !ok {
			return ParamTypeError(paramName, "bool")
		}
	case ParamTypeInt:
		switch v := value.(type) {
		case int, int32, int64:
			// ok
		case float64:
			if v != float64(int64(v)) {
				return ParamTypeError(paramName, "int, got float")
			}
		default:
			return ParamTypeError(paramName, "int")
		}
	case ParamTypeFloat:
		switch value.(type) {
		case float32, float64, int, int32, int64:
			// ok
		default:
			return ParamTypeError(paramName, "float")
		}
	default:
		return fmt.Errorf("unknown schema type '%s' for parameter '%s'", p.Type, paramName)
	}
	if len(p.Options) > 0 {
		s, ok := value.(string)
		if !ok {
			s = fmt.Sprint(value)
		}
		for _, o := range p.Options {
			if o == s {
				return nil
			}
		}
		return ParamOptionError(paramName, s, p.Options)
	}
	return nil
}

// DescribeParameter returns human description + effective UI hint (L2 SSOT for CLI/bind).
func DescribeParameter(paramName string, p ServiceParameter) (desc string, uiHint string) {
	uiHint = EffectiveUIHint(paramName, p)
	if len(p.Options) > 0 {
		return fmt.Sprintf("Options: [%s]", strings.Join(p.Options, ", ")), uiHint
	}
	switch p.Type {
	case ParamTypeBool:
		desc = fmt.Sprintf("Toggle to enable or disable the %s option.", paramName)
	case ParamTypeInt, ParamTypeFloat:
		desc = fmt.Sprintf("Enter a numerical value for %s.", paramName)
	case ParamTypeFile:
		if uiHint == "image_picker" {
			desc = fmt.Sprintf("Provide an image file path or capture a photo for %s.", paramName)
		} else {
			desc = fmt.Sprintf("Provide a file path or select a file for %s.", paramName)
		}
	default:
		desc = fmt.Sprintf("Provide a text value for %s.", paramName)
	}
	return desc, uiHint
}

const VFSURIPrefix = "vfs://"

// VFSURI builds a content-addressed VFS URI from a blob hash.
func VFSURI(hash string) string {
	return VFSURIPrefix + hash
}

// IsVFSURI reports whether s uses the vfs:// scheme.
func IsVFSURI(s string) bool {
	return strings.HasPrefix(s, VFSURIPrefix)
}

// IsStageableLocalPath reports whether v is a non-empty local filesystem path (not vfs://).
func IsStageableLocalPath(v any) (path string, ok bool) {
	pathStr, ok := v.(string)
	if !ok || pathStr == "" || IsVFSURI(pathStr) {
		return "", false
	}
	return pathStr, true
}

// ParseVFSURI extracts the blob hash from a vfs:// URI. ok is false if not a VFS URI.
func ParseVFSURI(s string) (hash string, ok bool) {
	if !IsVFSURI(s) {
		return "", false
	}
	hash = filepath.Base(strings.TrimPrefix(s, VFSURIPrefix))
	return hash, hash != "" && hash != "."
}

// OutputHashFromOutputs extracts a CAS blob hash from task outputs (L1).
// Prefers explicit output_hash, else first vfs:// value.
func OutputHashFromOutputs(outputs map[string]any) (hash string, name string, size int64) {
	if outputs == nil {
		return "", "", 0
	}
	if h, ok := outputs[OutputHashKey].(string); ok && h != "" {
		hash = h
		name, _ = outputs[OutputNameKey].(string)
		if sz, ok := outputs[OutputSizeKey].(float64); ok {
			size = int64(sz)
		}
		return hash, name, size
	}
	for k, v := range outputs {
		if pathStr, ok := v.(string); ok {
			if h, isVFS := ParseVFSURI(pathStr); isVFS {
				return h, k, 0
			}
		}
	}
	return "", "", 0
}

// ResultLocalPathKey is the canonical outputs key for a resolved local file path.
const ResultLocalPathKey = "result_path"

// Output metadata keys written by RewriteLocalFilePaths when annotateOutputs is true.
const (
	OutputHashKey = "output_hash"
	OutputNameKey = "output_name"
	OutputSizeKey = "output_size"
)

// ResultLocalPath returns a non-VFS local filesystem path from task outputs (L1 SSOT).
// Prefer result_path, then output_path. Empty if only hashes / vfs URIs remain.
func ResultLocalPath(outputs map[string]any) string {
	if outputs == nil {
		return ""
	}
	for _, key := range []string{ResultLocalPathKey, "output_path"} {
		if p, ok := outputs[key].(string); ok && p != "" && !IsVFSURI(p) {
			return p
		}
	}
	return ""
}

// LocalService is the on-disk services.json entry (SSOT).
type LocalService struct {
	Type   ServiceType   `json:"type"`
	Exec   string        `json:"exec,omitempty"`
	Schema ServiceSchema `json:"schema"`
}

type ServiceBid struct {
	NodeID          string        `json:"node_id"`
	NodeAddr        string        `json:"node_addr"`
	Schema          ServiceSchema `json:"schema"`
	EstimatedMillis int64         `json:"estimated_millis"`
	CPULoad         float64       `json:"cpu_load,omitempty"`
	MemPressure     float64       `json:"mem_pressure,omitempty"`
	CostUnits       int64         `json:"cost_units,omitempty"`
	PowerScore      int64         `json:"power_score,omitempty"`
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
	DisableUPnP          bool   `json:"disable_upnp,omitempty"`
	Logger               *slog.Logger
}

const (
	StrategyFastest  = "proxyma/strategy/fastest"
	StrategyCheapest = "proxyma/strategy/cheapest"
	StrategyLowPower = "proxyma/strategy/low_power"

	StrategyShortFastest  = "fastest"
	StrategyShortCheapest = "cheapest"
	StrategyShortLowPower = "low_power"
)

// SortStrategyShortOptions is the SSOT list of CLI/UI strategy option values.
func SortStrategyShortOptions() []string {
	return []string{StrategyShortFastest, StrategyShortCheapest, StrategyShortLowPower}
}

// StrategyShortName maps a canonical URN (or short/alias) to the short CLI name.
// Unknown values return empty string.
func StrategyShortName(s string) string {
	switch NormalizeSortStrategy(s) {
	case StrategyFastest:
		return StrategyShortFastest
	case StrategyCheapest:
		return StrategyShortCheapest
	case StrategyLowPower:
		return StrategyShortLowPower
	default:
		return ""
	}
}

// NormalizeSortStrategy maps short CLI names and legacy aliases to canonical strategy URNs.
// Empty input stays empty (selectBestServiceBid defaults to fastest).
func NormalizeSortStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", StrategyFastest, StrategyShortFastest:
		if s == "" {
			return ""
		}
		return StrategyFastest
	case StrategyCheapest, StrategyShortCheapest:
		return StrategyCheapest
	case StrategyLowPower, StrategyShortLowPower, "low-power", "lowpower":
		return StrategyLowPower
	default:
		return s
	}
}

// MatchServicePattern reports whether serviceName matches a subscription pattern.
// Patterns: exact name, "*", or prefix with trailing "*" / ".*" (e.g. "vision.*", "vision*").
func MatchServicePattern(pattern, serviceName string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" || pattern == serviceName {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return serviceName == prefix || strings.HasPrefix(serviceName, prefix+".")
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(serviceName, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

type PeerNotification struct {
	File   IndexEntry `json:"file"`
	Source string     `json:"source"`
}

type ServiceNotification struct {
	Action string        `json:"action"` // ActionAdd / ActionRemove
	NodeID string        `json:"node_id"`
	Schema ServiceSchema `json:"schema"`
}

type InviteRequest struct {
	ValidForMinutes int `json:"valid_for_minutes"`
}

type InviteResponse struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

type PeerIDRequest struct {
	ID string `json:"id"`
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

// RelayRequest encapsulates an HTTP request to be forwarded by the Sponsor
type RelayRequest struct {
	ReqID        string            `json:"req_id"`
	Target       string            `json:"target"` // PeerID
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers"`
	Body         []byte            `json:"body"`
	OriginPeerID string            `json:"origin_peer_id,omitempty"`
}

// NewRelayRequest builds a RelayRequest (L1). ReqID must be set by the caller (or via p2p.NewRelayRequest).
func NewRelayRequest(reqID, target, method, path string, body []byte, headers map[string]string) RelayRequest {
	return RelayRequest{
		ReqID:   reqID,
		Target:  target,
		Method:  method,
		Path:    path,
		Headers: headers,
		Body:    body,
	}
}

// RelayResponse encapsulates an HTTP response returned by the target node
type RelayResponse struct {
	ReqID      string            `json:"req_id"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
}

// ToHTTPResponse synthesizes an http.Response from a relay response (L1).
// Caller owns closing Body. req may be nil.
func (rr RelayResponse) ToHTTPResponse(req *http.Request) *http.Response {
	res := &http.Response{
		StatusCode:    rr.StatusCode,
		Status:        http.StatusText(rr.StatusCode),
		Body:          io.NopCloser(bytes.NewReader(rr.Body)),
		Header:        make(http.Header),
		ContentLength: int64(len(rr.Body)),
		Request:       req,
	}
	for k, v := range rr.Headers {
		res.Header.Set(k, v)
	}
	return res
}

type ProbeRequest struct {
	Address string `json:"address"`
}

type ProbeResponse struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// RotateTLSPayload carries re-signed PEMs pushed by the CA authority during CA
// rotation. Private keys never travel: each node keeps its own.
type RotateTLSPayload struct {
	CACert   string `json:"ca_cert"`
	NodeCert string `json:"node_cert"`
}

// APIMessage is the wire shape for success responses with a human-readable message.
type APIMessage struct {
	Message string `json:"message"`
}

// APIStatus is the wire shape for success responses with a machine status token.
type APIStatus struct {
	Status string `json:"status"`
}

// APITaskAck is the wire shape for task submit/callback acknowledgements.
type APITaskAck struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	JobID   string `json:"job_id"`
}

type PeerStatus struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Online  bool   `json:"online"`
	Error   string `json:"error,omitempty"`
}

type BandwidthStats struct {
	UploadSpeed   int64 `json:"upload_speed"`
	DownloadSpeed int64 `json:"download_speed"`
	TotalSent     int64 `json:"total_sent"`
	TotalReceived int64 `json:"total_received"`
}

