package protocol

// ServiceType is the transport/execution kind of a service.
type ServiceType string

const (
	ServiceTypeExec             ServiceType = "exec"
	ServiceTypeScript           ServiceType = "script"
	ServiceTypeGRPC             ServiceType = "grpc"
	ServiceTypeGRPCBidi         ServiceType = "grpc_bidi"
	ServiceTypeBidiGRPC         ServiceType = "bidi_grpc"
	ServiceTypeBidi             ServiceType = "bidi"
	ServiceTypeBidiStream       ServiceType = "bidi_stream"
	ServiceTypeServerStream     ServiceType = "server_stream"
	ServiceTypeHTTPServerStream ServiceType = "http_server_stream"
	ServiceTypeGRPCServerStream ServiceType = "grpc_server_stream" // legacy alias → server_stream
	ServiceTypeHTTPBidi         ServiceType = "http_bidi"          // HTTP NDJSON bidi (preferred name)
	ServiceTypeWebRTC           ServiceType = "webrtc"             // WebRTC DataChannel JSON stream
	ServiceTypeScreen           ServiceType = "screen"             // server-stream of media frames (fake/MJPEG)
)

// serviceTypeSpec declares one canonical service type: the names that resolve to
// it and whether it streams.
type serviceTypeSpec struct {
	Canonical ServiceType
	Aliases   []ServiceType
	Streaming bool
}

// serviceTypeSpecs is the SSOT for alias resolution and streaming semantics.
// Adding a type means adding one row here plus one handler builder in
// internal/compute (compute cannot be imported from protocol).
//
// Names carrying "grpc_" are legacy: the transport is HTTP/NDJSON, not protobuf gRPC.
var serviceTypeSpecs = []serviceTypeSpec{
	{Canonical: ServiceTypeExec},
	{Canonical: ServiceTypeScript},
	{Canonical: ServiceTypeGRPC},
	{
		Canonical: ServiceTypeGRPCBidi,
		Aliases:   []ServiceType{ServiceTypeBidiGRPC, ServiceTypeBidiStream, ServiceTypeHTTPBidi},
		Streaming: true,
	},
	{Canonical: ServiceTypeBidi, Streaming: true},
	{
		Canonical: ServiceTypeServerStream,
		Aliases:   []ServiceType{ServiceTypeHTTPServerStream, ServiceTypeGRPCServerStream},
		Streaming: true,
	},
	{Canonical: ServiceTypeWebRTC, Streaming: true},
	{Canonical: ServiceTypeScreen, Streaming: true},
}

var serviceTypeIndex = buildServiceTypeIndex()

func buildServiceTypeIndex() map[ServiceType]serviceTypeSpec {
	index := make(map[ServiceType]serviceTypeSpec, len(serviceTypeSpecs)*2)
	for _, spec := range serviceTypeSpecs {
		index[spec.Canonical] = spec
		for _, alias := range spec.Aliases {
			index[alias] = spec
		}
	}
	return index
}

// Normalize maps aliases to their canonical type. Unknown types pass through
// unchanged so BuildHandler can report them.
func (t ServiceType) Normalize() ServiceType {
	if spec, ok := serviceTypeIndex[t]; ok {
		return spec.Canonical
	}
	return t
}

func (t ServiceType) IsStreaming() bool {
	spec, ok := serviceTypeIndex[t]
	return ok && spec.Streaming
}

func (t ServiceType) String() string {
	return string(t)
}

// KnownServiceTypes returns every canonical type, for consistency tests and help text.
func KnownServiceTypes() []ServiceType {
	out := make([]ServiceType, 0, len(serviceTypeSpecs))
	for _, spec := range serviceTypeSpecs {
		out = append(out, spec.Canonical)
	}
	return out
}
