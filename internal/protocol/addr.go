package protocol

import (
	"net"
	"strconv"
	"strings"
)

// PeerLocalDomain is the DNS suffix for in-cluster peer virtual hosts.
const PeerLocalDomain = "proxyma.local"

// PeerLocalHost builds the virtual host for a peer ID (L1).
func PeerLocalHost(peerID string) string {
	return peerID + "." + PeerLocalDomain
}

// ParsePeerLocalHost extracts the peer ID from a .proxyma.local host.
// host should be the hostname without port (e.g. from url.URL.Hostname()).
func ParsePeerLocalHost(host string) (peerID string, ok bool) {
	suffix := "." + PeerLocalDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(host, suffix)
	if id == "" || strings.Contains(id, ".") {
		return "", false
	}
	return id, true
}

// PeerHTTPURL builds http://<id>.proxyma.local/<path> (L2).
func PeerHTTPURL(peerID, path string) string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return "http://" + PeerLocalHost(peerID) + "/"
	}
	return "http://" + PeerLocalHost(peerID) + "/" + path
}

// PeerHTTPSURL builds https://<id>.proxyma.local<path> (L2).
// path should be an absolute path including a leading slash (e.g. PathServicesCallback).
func PeerHTTPSURL(peerID, path string) string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "https://" + PeerLocalHost(peerID) + path
}

// SchemeAddr builds scheme://host:port (L1 SSOT). IPv6 literals are bracketed,
// so callers must not pre-format the host.
func SchemeAddr(scheme, host, port string) string {
	return scheme + "://" + net.JoinHostPort(host, port)
}

// HTTPSAddr builds the canonical node URL (L2 over SchemeAddr).
func HTTPSAddr(host, port string) string {
	return SchemeAddr("https", host, port)
}

// HTTPSAddrPort is HTTPSAddr for a numeric TCP port.
func HTTPSAddrPort(host string, port uint16) string {
	return HTTPSAddr(host, strconv.Itoa(int(port)))
}
