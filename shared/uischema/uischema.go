package uischema

import (
	"encoding/json"

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
				Name:        "upload",
				Title:       "Upload File",
				Description: "Upload a local file into the VFS registry",
				OutputType:  "text",
				Parameters: []ParameterDetail{
					{Name: "path", Type: "string", Required: true, Description: "Absolute or relative path to the local file", UIHint: "file_picker"},
					{Name: "name", Type: "string", Required: false, Description: "Optional destination filename inside VFS"},
				},
			},
			{
				Name:        "subscribe",
				Title:       "Subscribe to File",
				Description: "Subscribe to download updates for a VFS file",
				OutputType:  "text",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "unsubscribe",
				Title:       "Unsubscribe from File",
				Description: "Unsubscribe from updates for a VFS file",
				OutputType:  "text",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "delete",
				Title:       "Delete File",
				Description: "Mark a file as deleted in the VFS registry",
				OutputType:  "text",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "purge",
				Title:       "Purge Cache",
				Description: "Purge the local physical cache of a VFS file",
				OutputType:  "text",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "open",
				Title:       "Open File",
				Description: "Download on-demand if missing and open or view a VFS file",
				OutputType:  "text",
				Parameters:  []ParameterDetail{vfsNameParam},
			},
			{
				Name:        "sync",
				Title:       "Sync VFS",
				Description: "Trigger VFS synchronization sequence",
				OutputType:  "text",
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
				Description: "Generate an invite token valid for 15 minutes",
				OutputType:  "text",
			},
			{
				Name:        "join",
				Title:       "Join Cluster",
				Description: "Join an existing cluster, writes configuration, and starts the node",
				OutputType:  "text",
				Parameters: []ParameterDetail{
					{Name: "token", Type: "string", Required: true, Description: "Smart Invite Token generated by a sponsor"},
					{Name: "node_id", Type: "string", Required: false, Description: "Optional Node ID (auto-generated if empty)"},
					{Name: "port", Type: "string", Required: true, Description: "Listening port for connection", UIHint: "text", DefaultValue: protocol.DefaultTCPPort},
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
				Columns: []TableColumn{
					{Header: "SERVICE NAME", FieldSelector: ".", Format: "string"},
				},
			},
			{
				Name:        "add",
				Title:       "Add Service",
				Description: "Add a new service to the local node",
				OutputType:  "text",
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
				Name:        "remove",
				Title:       "Remove Service",
				Description: "Remove a service from the local node",
				OutputType:  "text",
				Parameters:  []ParameterDetail{svcNameParam},
			},
			{
				Name:        "run",
				Title:       "Run Service",
				Description: "Execute a compute service (unary, streaming, or file transformation)",
				OutputType:  "json",
				Parameters: []ParameterDetail{
					{Name: "name", Type: "string", Required: true, Description: "Service name to run"},
					{Name: "inputs", Type: "string", Required: false, Description: "Service inputs in 'key1=val1,key2=val2' format or JSON object"},
					{Name: "payload", Type: "string", Required: false, Description: "Legacy alias for service inputs in JSON format"},
				},
			},
			{
				Name:        "status",
				Title:       "Task Status",
				Description: "Query status of a specific task execution",
				OutputType:  "json",
				Parameters: []ParameterDetail{
					{Name: "task_id", Type: "string", Required: false, Description: "ID of the task to query"},
				},
			},
			{
				Name:        "add_pipeline",
				Title:       "Add Pipeline",
				Description: "Add a service pipeline schema to the cluster",
				OutputType:  "text",
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: true, Description: "Unique pipeline identifier"},
					{Name: "schema-file", Type: "string", Required: true, Description: "Path to JSON file containing the pipeline schema", UIHint: "file_picker"},
				},
			},
			{
				Name:        "remove_pipeline",
				Title:       "Remove Pipeline",
				Description: "Remove a pipeline schema from the cluster",
				OutputType:  "text",
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: true, Description: "Unique pipeline identifier"},
				},
			},
			{
				Name:        "get_pipeline",
				Title:       "Get Pipeline Schema",
				Description: "Retrieve a pipeline schema JSON by ID",
				OutputType:  "json",
				Parameters: []ParameterDetail{
					{Name: "id", Type: "string", Required: true, Description: "Unique pipeline identifier"},
				},
			},
			{
				Name:        "clone_pipeline",
				Title:       "Clone Pipeline Schema",
				Description: "Clone and customize a pipeline schema for local target node execution",
				OutputType:  "text",
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

// GetRegistryJSON returns the complete UI Schema registry in JSON format.
func GetRegistryJSON() string {
	b, _ := json.Marshal(Registry)
	return string(b)
}

// GetDomainJSON returns a specific domain metadata by name in JSON format.
func GetDomainJSON(domainName string) string {
	for _, d := range Registry {
		if d.Name == domainName {
			b, _ := json.Marshal(d)
			return string(b)
		}
	}
	return "{}"
}
