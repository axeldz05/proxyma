package protocol

import "strings"

// HTTP API paths (SSOT). Always include leading slash for mux / URL.Path / relay Path.
const (
	PathUpload           = "/upload"
	PathDownloadPrefix   = "/download/"
	PathFile             = "/file"
	PathManifest         = "/manifest"
	PathSubscribe        = "/subscribe"
	PathNotify           = "/notify"
	PathServicesBid      = "/services/bid"
	PathServicesSubmit   = "/services/submit"
	PathServicesStream   = "/services/stream"
	PathServicesCallback = "/services/callback"
	PathServicesNotify   = "/services/notify"
	PathServices         = "/services"
	PathSchemasNotify    = "/schemas/notify"
	PathPeers            = "/peers"
	PathPeersAnnounce    = "/peers/announce"
	PathPeersAdd         = "/peers/add"
	PathPeersLeave       = "/peers/leave"
	PathPeersOffline     = "/peers/offline"
	PathPeersInvite      = "/peers/invite"
	PathPeersProbe       = "/peers/probe"
	PathClusterJoin      = "/cluster/join"
	PathClusterRotate    = "/cluster/rotate"
	PathRelayPoll        = "/relay/poll"
	PathRelayForward     = "/relay/forward"
	PathRelayReply       = "/relay/reply"
	PathTelemetry        = "/telemetry"
	PathHolePunchInit    = "/holepunch/init"
)

// MaxRelayBodyBytes is the SSOT size limit for relay-forwarded request bodies.
const MaxRelayBodyBytes = 65536

// PathRel returns path without a leading slash for peer-client URL join (L1).
func PathRel(p string) string {
	return strings.TrimPrefix(p, "/")
}

// ServicesPrefix is the path prefix used for bandwidth categorization of service RPCs.
const ServicesPrefix = "/services/"
