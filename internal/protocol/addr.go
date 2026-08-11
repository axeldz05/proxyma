package protocol

import (
	"net"
	"strconv"
)

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
