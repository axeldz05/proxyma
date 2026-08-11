package protocol

import (
	"net"
	"strconv"
)

// HTTPSAddr builds the canonical node URL (L1 SSOT). IPv6 literals are bracketed,
// so callers must not pre-format the host.
func HTTPSAddr(host, port string) string {
	return "https://" + net.JoinHostPort(host, port)
}

// HTTPSAddrPort is HTTPSAddr for a numeric TCP port.
func HTTPSAddrPort(host string, port uint16) string {
	return HTTPSAddr(host, strconv.Itoa(int(port)))
}
