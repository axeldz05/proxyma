package uischema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"proxyma/internal/protocol"
)

// ParameterDetail defines a parameter for an action
type ParameterDetail struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"` // "string", "int", "bool", "file"
	Required     bool     `json:"required"`
	Description  string   `json:"description"`
	UIHint       string   `json:"uiHint,omitempty"` // "file_picker", "image_picker", "audio_picker", "audio_stream", "live_stream", "text", "password", "dropdown"
	DefaultValue string   `json:"defaultValue,omitempty"`
	Options      []string `json:"options,omitempty"`
}

// TableColumn defines how a column should be formatted in a table view
type TableColumn struct {
	Header        string `json:"header"`
	FieldSelector string `json:"fieldSelector"`
	Format        string `json:"format"` // "string", "bytes", "boolean", "speed", "status"
}

// ActionDetail defines a user-facing action inside a domain
type ActionDetail struct {
	Domain      string            `json:"domain"`
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Parameters  []ParameterDetail `json:"parameters"`
	OutputType  string            `json:"outputType"` // "table", "text", "json", "stream"
	Columns     []TableColumn     `json:"columns,omitempty"`
	// UnixAction is the daemon IPC action string (SSOT). Empty = local-only (no unix).
	UnixAction string `json:"unixAction,omitempty"`
	// SuccessMessage is used when outputType is "text" and the handler returns nil/empty.
	// Supports {{param}} placeholders from args and {{result}} from a string return value.
	SuccessMessage string `json:"successMessage,omitempty"`
	// Hidden excludes the action from CLI/Android UI export while keeping it in the SSOT for IPC.
	Hidden bool `json:"hidden,omitempty"`
	// Surfaces restricts UI surfaces when non-empty (e.g. "cli", "android"). Empty = all visible surfaces.
	Surfaces []string `json:"surfaces,omitempty"`
}

// DomainDetail groups actions under a specific category/domain
type DomainDetail struct {
	Name    string         `json:"name"`
	Title   string         `json:"title"`
	Actions []ActionDetail `json:"actions"`
}

// Shared reusable parameter definitions to avoid code duplication (semantic compression)
var (
	vfsNameParam = ParameterDetail{
		Name:        "name",
		Type:        "string",
		Required:    true,
		Description: "VFS filename",
	}
	svcNameParam = ParameterDetail{
		Name:        "name",
		Type:        "string",
		Required:    true,
		Description: "Service name",
	}
	pipelineIDParam = ParameterDetail{
		Name:        "id",
		Type:        "string",
		Required:    true,
		Description: "Unique pipeline identifier",
	}
)

// Registry is the global single source of truth for all user actions
var Registry = []DomainDetail{
	{
		Name:  "storage",
		Title: "Virtual File System & Storage",
		Actions: []ActionDetail{
			{
				Name:        "list",
				Title:       "List Files",
				Description: "List all files in the virtual file system snapshot",
				OutputType:  "table",
				UnixAction:  "vfs_list",
				Columns: []TableColumn{
					{Header: "NAME", FieldSelector: "name", Format: "string"},
					{Header: "VERSION", FieldSelector: "version", Format: "string"},
					{Header: "SIZE", FieldSelector: "size", Format: "bytes"},
					{Header: "SUBSCRIBED", FieldSelector: "subscribed", Format: "boolean"},
					{Header: "LOCAL", FieldSelector: "hasLocal", Format: "boolean"},
					{Header: "STATUS", FieldSelector: "deleted", Format: "status"},
					{Header: "HASH", FieldSelector: "hash", Format: "string"},
				},
			},
			{
				Name:           "upload",
				Title:          "Upload File",
				Description:    "Upload a local file into the VFS registry",
				OutputType:     "text",
				UnixAction:     "vfs_upload",
				SuccessMessage: "File '{{name}}' uploaded successfully to VFS.",
				Parameters: []ParameterDetail{
					{Name: "path", Type: "string", Required: true, Description: "Absolute or relative path to the local file", UIHint: "file_picker"},
					{Name: "name", Type: "string", Required: false, Description: "Optional destination filename inside VFS"},
				},
			},
			{
				Name:           "subscribe",
				Title:          "Subscribe to File",
				Description:    "Subscribe to download updates for a VFS file",
				OutputType:     "text",
				UnixAction:     "vfs_subscribe",
				SuccessMessage: "Subscribed to file '{{name}}'. Synchronization triggered.",
				Parameters:     []ParameterDetail{vfsNameParam},
			},
			{
				Name:           "unsubscribe",
				Title:          "Unsubscribe from File",
				Description:    "Unsubscribe from updates for a VFS file",
				OutputType:     "text",
				UnixAction:     "vfs_unsubscribe",
				SuccessMessage: "Unsubscribed from file '{{name}}'.",
				Parameters:     []ParameterDetail{vfsNameParam},
			},
			{
				Name:           "delete",
				Title:          "Delete File",
				Description:    "Mark a file as deleted in the VFS registry",
				OutputType:     "text",
				UnixAction:     "vfs_delete",
				SuccessMessage: "File '{{name}}' marked as deleted in VFS registry.",
				Parameters:     []ParameterDetail{vfsNameParam},
			},
			{
				Name:           "purge",
				Title:          "Purge Cache",
				Description:    "Purge the local physical cache of a VFS file",
				OutputType:     "text",
				UnixAction:     "vfs_purge",
				SuccessMessage: "Physical cache for file '{{name}}' purged from disk.",
				Parameters:     []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "open",
				Title:       "Open File",
				Description: "Download on-demand if missing and open or view a VFS file",
				OutputType:  "text",
				UnixAction:  "vfs_fetch",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:           "sync",
				Title:          "Sync VFS",
				Description:    "Trigger VFS synchronization sequence",
				OutputType:     "text",
				UnixAction:     "sync",
				SuccessMessage: "Synchronization triggered successfully.",
			},
		},
	},
	{
		Name:  "peers",
		Title: "Connected Peers",
		Actions: []ActionDetail{
			{
				Name:        "list",
				Title:       "List Peers",
				Description: "View connected cluster peers and status",
				OutputType:  "table",
				UnixAction:  "peers",
				Columns: []TableColumn{
					{Header: "PEER ID", FieldSelector: "id", Format: "string"},
					{Header: "ADDRESS", FieldSelector: "address", Format: "string"},
					{Header: "STATUS", FieldSelector: "online", Format: "status"},
					{Header: "ERROR", FieldSelector: "error", Format: "string"},
				},
			},
		},
	},
	{
		Name:  "cluster",
		Title: "Cluster Management",
		Actions: []ActionDetail{
			{
				Name:        "invite",
				Title:       "Generate Invite",
				Description: fmt.Sprintf("Generate an invite token valid for %d minutes", protocol.DefaultInviteMinutes),
				OutputType:  "text",
				UnixAction:  "invite_generate",
			},
			{
				Name:        "join",
				Title:       "Join Cluster",
				Description: "Join an existing cluster, writes configuration, and starts the node",
				OutputType:  "text",
				// No UnixAction: each supported surface dispatches its local JoinCluster escape.
				Surfaces: []string{"cli", "android"},
				Parameters: []ParameterDetail{
					{Name: "token", Type: "string", Required: true, Description: "Smart Invite Token generated by a sponsor"},
					{Name: "node_id", Type: "string", Required: false, Description: "Optional Node ID (auto-generated if empty)"},
					{Name: "port", Type: "string", Required: false, Description: "Listening port for connection", UIHint: "text", DefaultValue: protocol.DefaultTCPPort},
				},
			},
		},
	},
	{
		Name:  "telemetry",
		Title: "Telemetry & Logs",
		Actions: []ActionDetail{
			{
				Name:        "logs",
				Title:       "System Logs",
				Description: "Get the centralized system logs",
				OutputType:  "table",
				UnixAction:  "logs",
				Columns: []TableColumn{
					{Header: "TIME", FieldSelector: "timestamp", Format: "string"},
					{Header: "LEVEL", FieldSelector: "level", Format: "string"},
					{Header: "MESSAGE", FieldSelector: "message", Format: "string"},
				},
			},
			{
				Name:        "stats",
				Title:       "Bandwidth Stats",
				Description: "View real-time bandwidth speeds and totals",
				OutputType:  "table",
				UnixAction:  "bandwidth",
				Columns: []TableColumn{
					{Header: "METRIC", FieldSelector: "metric", Format: "string"},
					{Header: "VALUE", FieldSelector: "value", Format: "string"},
				},
			},
		},
	},
	{
		Name:  "service",
		Title: "Compute Services",
		Actions: []ActionDetail{
			{
				Name:        "discover",
				Title:       "Discover Services",
				Description: "Query active services in the cluster",
				OutputType:  "table",
				UnixAction:  "service_discover",
				Columns: []TableColumn{
					{Header: "SERVICE NAME", FieldSelector: ".", Format: "string"},
				},
			},
			{
				Name:           "add",
				Title:          "Add Service",
				Description:    "Add a new service to the local node",
				OutputType:     "text",
				UnixAction:     "service_add",
				SuccessMessage: "Service '{{name}}' added successfully. Restart the node to apply changes.",
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name or JSON file path"},
					{Name: "type", Type: "string", Required: false, Description: "Service type (exec, grpc)", UIHint: "text", DefaultValue: "exec"},
					{Name: "exec", Type: "string", Required: false, Description: "Command to execute or gRPC address"},
					{Name: "desc", Type: "string", Required: false, Description: "Short description of the service"},
					{Name: "param", Type: "string", Required: false, Description: "Parameters in format 'name:type,name2:type'"},
					{Name: "no-required", Type: "string", Required: false, Description: "List of optional parameters separated by comma"},
					{Name: "schema-file", Type: "string", Required: false, Description: "Path to a JSON file containing the complete ServiceSchema", UIHint: "file_picker"},
				},
			},
			{
				Name:           "remove",
				Title:          "Remove Service",
				Description:    "Remove a service from the local node",
				OutputType:     "text",
				UnixAction:     "service_remove",
				SuccessMessage: "Service '{{name}}' removed successfully. Restart the node to apply changes.",
				Parameters:     []ParameterDetail{svcNameParam},
			},
			{
				Name:           "subscribe",
				Title:          "Subscribe Service",
				Description:    "Subscribe to remote service notifies by name or pattern (e.g. vision.*)",
				OutputType:     "text",
				UnixAction:     "service_subscribe",
				SuccessMessage: "Subscribed to service pattern '{{name}}'.",
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name or pattern (suffix * / .*)"},
				},
			},
			{
				Name:           "unsubscribe",
				Title:          "Unsubscribe Service",
				Description:    "Stop filtering interest for a service name or pattern",
				OutputType:     "text",
				UnixAction:     "service_unsubscribe",
				SuccessMessage: "Unsubscribed from service pattern '{{name}}'.",
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name or pattern to drop"},
				},
			},
			{
				Name:        "run",
				Title:       "Run Service",
				Description: "Execute a compute service (unary, streaming, or file transformation)",
				OutputType:  "json",
				UnixAction:  "service_run",
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name to run"},
					{Name: "inputs", Type: "string", Required: false, Description: "Service inputs in 'key1=val1,key2=val2' format or JSON object"},
					{Name: "payload", Type: "string", Required: false, Description: "Legacy alias for service inputs in JSON format"},
					{Name: "strategy", Type: "string", Required: false, Description: "Bid sort strategy", UIHint: "dropdown", Options: protocol.SortStrategyShortOptions()},
				},
			},
			{
				Name:        "stream",
				Title:       "Stream Service",
				Description: "Execute a streaming compute service (IPC)",
				OutputType:  "stream",
				UnixAction:  "service_stream",
				Hidden:      true,
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name to stream"},
					{Name: "payload", Type: "string", Required: false, Description: "Service inputs JSON"},
				},
			},
			{
				Name:        "detail",
				Title:       "Service Detail",
				Description: "Resolve raw ServiceSchema for a service (IPC)",
				OutputType:  "json",
				UnixAction:  "service_detail",
				Hidden:      true,
				Parameters:  []ParameterDetail{svcNameParam},
			},
			{
				Name:        "status",
				Title:       "Task Status",
				Description: "Query status of a specific task execution",
				OutputType:  "json",
				UnixAction:  "service_status",
				Parameters: []ParameterDetail{
					{Name: "task_id", Type: "string", Required: false, Description: "ID of the task to query"},
				},
			},
			{
				Name:           "add_pipeline",
				Title:          "Add Pipeline",
				Description:    "Add a service pipeline schema to the cluster",
				OutputType:     "text",
				UnixAction:     "pipeline_add",
				SuccessMessage: "Pipeline added successfully",
				Parameters: []ParameterDetail{
					pipelineIDParam,
					{Name: "schema", Type: "string", Required: false, Description: "Pipeline schema JSON (alternative to schema-file)"},
					{Name: "schema-file", Type: "string", Required: false, Description: "Path to JSON file containing the pipeline schema", UIHint: "file_picker"},
				},
			},
			{
				Name:           "validate_pipeline",
				Title:          "Validate Pipeline",
				Description:    "Validate a pipeline schema JSON without saving (IPC)",
				OutputType:     "text",
				UnixAction:     "pipeline_validate",
				Hidden:         true,
				SuccessMessage: "Pipeline schema is valid",
				Parameters: []ParameterDetail{
					{Name: "schema", Type: "string", Required: true, Description: "Pipeline schema JSON"},
				},
			},
			{
				Name:           "remove_pipeline",
				Title:          "Remove Pipeline",
				Description:    "Remove a pipeline schema from the cluster",
				OutputType:     "text",
				UnixAction:     "pipeline_remove",
				SuccessMessage: "Pipeline removed successfully",
				Parameters:     []ParameterDetail{pipelineIDParam},
			},
			{
				Name:        "get_pipeline",
				Title:       "Get Pipeline Schema",
				Description: "Retrieve a pipeline schema JSON by ID",
				OutputType:  "json",
				UnixAction:  "pipeline_get",
				Parameters:  []ParameterDetail{pipelineIDParam},
			},
			{
				Name:        "clone_pipeline",
				Title:       "Clone Pipeline Schema",
				Description: "Clone and customize a pipeline schema for local target node execution",
				OutputType:  "json",
				UnixAction:  "pipeline_clone",
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: true, Description: "Existing pipeline identifier to clone"},
					{Name: "new_id", Type: "string", Required: false, Description: "New ID for cloned pipeline"},
					{Name: "target_node", Type: "string", Required: false, Description: "Target node ID to assign ($local for current node)"},
				},
			},
			{
				Name:        "list_pipelines",
				Title:       "List Pipelines",
				Description: "List all registered pipeline schemas",
				OutputType:  "table",
				UnixAction:  "pipeline_list",
				Columns: []TableColumn{
					{Header: "PIPELINE ID", FieldSelector: "id", Format: "string"},
					{Header: "VERSION", FieldSelector: "version", Format: "string"},
					{Header: "STEPS COUNT", FieldSelector: "steps", Format: "string"},
				},
			},
			{
				Name:        "run_pipeline",
				Title:       "Run Pipeline",
				Description: "Execute a pipeline across the cluster",
				OutputType:  "json",
				UnixAction:  "service_run",
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: true, Description: "Pipeline ID to run"},
					{Name: "payload", Type: "string", Required: false, Description: "Initial input parameters payload in JSON format"},
				},
			},
			{
				Name:        "edit_pipeline",
				Title:       "Edit Pipeline",
				Description: "Launch the interactive TUI pipeline editor",
				OutputType:  "text",
				// No UnixAction / no daemon handler: CLI TUI escape only.
				Surfaces: []string{"cli"},
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: false, Description: "Pipeline ID to edit from daemon"},
					{Name: "file", Type: "string", Required: false, Description: "Path to local JSON schema file to load and edit", UIHint: "file_picker"},
				},
			},
		},
	},
}

func init() {
	// Programmatically set the Domain field of each action to avoid redundancy in definition
	for i := range Registry {
		for j := range Registry[i].Actions {
			Registry[i].Actions[j].Domain = Registry[i].Name
		}
	}
}

// Key returns "domain.action" for an ActionDetail.
func (a ActionDetail) Key() string {
	return a.Domain + "." + a.Name
}

// VisibleOn reports whether the action should appear on a UI surface.
func (a ActionDetail) VisibleOn(surface string) bool {
	if a.Hidden {
		return false
	}
	if len(a.Surfaces) == 0 {
		return true
	}
	for _, s := range a.Surfaces {
		if s == surface {
			return true
		}
	}
	return false
}

// FindAction looks up an action by domain and name.
func FindAction(domain, name string) (ActionDetail, bool) {
	for _, d := range Registry {
		if d.Name != domain {
			continue
		}
		for _, a := range d.Actions {
			if a.Name == name {
				return a, true
			}
		}
	}
	return ActionDetail{}, false
}

// FindUnixAction resolves the canonical action metadata for a raw daemon IPC action.
// When aliases share an IPC action, the first registry declaration is canonical.
func FindUnixAction(unixAction string) (ActionDetail, bool) {
	for _, domain := range Registry {
		for _, action := range domain.Actions {
			if action.UnixAction == unixAction {
				return action, true
			}
		}
	}
	return ActionDetail{}, false
}

// UnixActionFor returns the unix IPC action string for domain.action.
func UnixActionFor(domain, name string) (string, bool) {
	a, ok := FindAction(domain, name)
	if !ok || a.UnixAction == "" {
		return "", false
	}
	return a.UnixAction, true
}

// MustUnixAction returns the unix action or panics (for handler registration).
func MustUnixAction(domain, name string) string {
	ua, ok := UnixActionFor(domain, name)
	if !ok {
		panic(fmt.Sprintf("uischema: missing UnixAction for %s.%s", domain, name))
	}
	return ua
}

// AllUnixActions maps unixAction → first "domain.action" that declares it.
func AllUnixActions() map[string]string {
	out := make(map[string]string)
	for _, d := range Registry {
		for _, a := range d.Actions {
			if a.UnixAction == "" {
				continue
			}
			if _, exists := out[a.UnixAction]; !exists {
				out[a.UnixAction] = a.Key()
			}
		}
	}
	return out
}

// VisibleRegistry returns a copy of Registry with Hidden actions removed (and Surfaces filtered when surface != "").
func VisibleRegistry(surface string) []DomainDetail {
	out := make([]DomainDetail, 0, len(Registry))
	for _, d := range Registry {
		vis := DomainDetail{Name: d.Name, Title: d.Title}
		for _, a := range d.Actions {
			if surface == "" {
				if a.Hidden {
					continue
				}
			} else if !a.VisibleOn(surface) {
				continue
			}
			vis.Actions = append(vis.Actions, a)
		}
		if len(vis.Actions) > 0 {
			out = append(out, vis)
		}
	}
	return out
}

// ApplyDefaults fills empty args from parameter DefaultValue.
func ApplyDefaults(action ActionDetail, args map[string]string) map[string]string {
	if args == nil {
		args = make(map[string]string)
	}
	out := make(map[string]string, len(args)+len(action.Parameters))
	for k, v := range args {
		out[k] = v
	}
	for _, p := range action.Parameters {
		if out[p.Name] == "" && p.DefaultValue != "" {
			out[p.Name] = p.DefaultValue
		}
	}
	return out
}

// MissingRequired returns names of required parameters that are still empty after defaults.
func MissingRequired(action ActionDetail, args map[string]string) []string {
	args = ApplyDefaults(action, args)
	var missing []string
	for _, p := range action.Parameters {
		if p.Required && strings.TrimSpace(args[p.Name]) == "" {
			missing = append(missing, p.Name)
		}
	}
	return missing
}

// ValidateActionArgs applies defaults, checks required params, types, and Options membership.
// Returns the args map after defaults (safe to use for dispatch).
func ValidateActionArgs(action ActionDetail, args map[string]string) (map[string]string, error) {
	args = ApplyDefaults(action, args)
	if missing := MissingRequired(action, args); len(missing) > 0 {
		return nil, fmt.Errorf("missing required parameter(s): %s", strings.Join(missing, ", "))
	}
	for _, p := range action.Parameters {
		v := strings.TrimSpace(args[p.Name])
		if v == "" {
			continue
		}
		if err := validateAdminParam(p, v); err != nil {
			return nil, err
		}
	}
	return args, nil
}

// NormalizeActionArgs maps surface aliases to the transport-neutral daemon contract.
func NormalizeActionArgs(domain, action string, args map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(args)+2)
	for key, value := range args {
		out[key] = value
	}
	switch domain + "." + action {
	case "storage.upload":
		if out["name"] == "" && out["path"] != "" {
			out["name"] = filepath.Base(out["path"])
		}
	case "service.run", "service.run_pipeline", "service.stream":
		serviceName := firstNonEmpty(out["service"], out["name"], out["id"])
		out["service"] = serviceName
		if out["name"] == "" {
			out["name"] = serviceName
		}
		payload := firstNonEmpty(out["payload"], out["inputs"], out["param"])
		if out["input"] != "" && payload == "" {
			payload = "input_path=" + out["input"]
		}
		out["payload"] = NormalizePayloadJSON(payload)
	case "service.add_pipeline":
		if out["schema"] == "" && out["schema-file"] != "" {
			schemaBytes, err := os.ReadFile(out["schema-file"])
			if err != nil {
				return nil, err
			}
			out["schema"] = string(schemaBytes)
		}
		if out["schema"] != "" {
			var schema protocol.PipelineSchema
			if err := json.Unmarshal([]byte(out["schema"]), &schema); err != nil {
				return nil, fmt.Errorf("invalid pipeline schema json: %w", err)
			}
			if out["id"] != "" {
				schema.ID = out["id"]
			}
			normalized, _ := json.Marshal(schema)
			out["schema"] = string(normalized)
		}
		if strings.TrimSpace(out["schema"]) == "" {
			return nil, fmt.Errorf("schema or schema-file required")
		}
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func validateAdminParam(p ParameterDetail, v string) error {
	switch p.Type {
	case "bool":
		if v != "true" && v != "false" && v != "1" && v != "0" {
			return protocol.ParamTypeError(p.Name, "bool")
		}
	case "int":
		if !intStringOK(v) {
			return protocol.ParamTypeError(p.Name, "int")
		}
	case "string", "file", "":
		// ok
	default:
		// Unknown admin types treated as string (forward-compatible).
	}
	if len(p.Options) == 0 {
		return nil
	}
	if optionAllowed(p.Name, p.Options, v) {
		return nil
	}
	return protocol.ParamOptionError(p.Name, v, p.Options)
}

func intStringOK(v string) bool {
	if v == "" {
		return false
	}
	i := 0
	if v[0] == '+' || v[0] == '-' {
		if len(v) == 1 {
			return false
		}
		i = 1
	}
	for ; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

func optionAllowed(paramName string, options []string, value string) bool {
	for _, o := range options {
		if o == value {
			return true
		}
	}
	if paramName == "strategy" {
		short := protocol.StrategyShortName(value)
		if short == "" {
			return false
		}
		for _, o := range options {
			if o == short {
				return true
			}
		}
	}
	return false
}

// FormatSuccessMessage expands SuccessMessage placeholders {{key}} from args and {{result}}.
func FormatSuccessMessage(action ActionDetail, args map[string]string, result string) string {
	msg := action.SuccessMessage
	if msg == "" {
		if result != "" {
			return result
		}
		return ""
	}
	out := msg
	for k, v := range args {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	out = strings.ReplaceAll(out, "{{result}}", result)
	return out
}

// GetRegistryJSON returns the UI-visible Schema registry in JSON format.
func GetRegistryJSON() string {
	return GetRegistryJSONForSurface("")
}

// GetRegistryJSONForSurface returns only actions visible on one UI surface.
func GetRegistryJSONForSurface(surface string) string {
	b, _ := json.Marshal(VisibleRegistry(surface))
	return string(b)
}

// GetDomainJSON returns a specific domain metadata by name in JSON format (visible actions only).
func GetDomainJSON(domainName string) string {
	for _, d := range VisibleRegistry("") {
		if d.Name == domainName {
			b, _ := json.Marshal(d)
			return string(b)
		}
	}
	return "{}"
}
