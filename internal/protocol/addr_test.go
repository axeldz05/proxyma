package protocol_test

import (
	"testing"

	"proxyma/internal/protocol"
)

func TestHTTPSAddr(t *testing.T) {
	cases := []struct {
		name string
		host string
		port string
		want string
	}{
		{"ipv4", "192.168.1.10", "8080", "https://192.168.1.10:8080"},
		{"hostname", "node-a", "8443", "https://node-a:8443"},
		{"dns", "node-a.proxyma.local", "8080", "https://node-a.proxyma.local:8080"},
		{"loopback", "127.0.0.1", "8080", "https://127.0.0.1:8080"},
		{"ipv6", "2001:db8::1", "8080", "https://[2001:db8::1]:8080"},
		{"ipv6 loopback", "::1", "8080", "https://[::1]:8080"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocol.HTTPSAddr(tc.host, tc.port); got != tc.want {
				t.Errorf("HTTPSAddr(%q, %q) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestSchemeAddr(t *testing.T) {
	if got := protocol.SchemeAddr("http", "10.0.0.1", "8080"); got != "http://10.0.0.1:8080" {
		t.Errorf("unexpected address %q", got)
	}
	// A peer announcing over IPv6 must not produce https://fe80::1:8443.
	if got := protocol.SchemeAddr("https", "fe80::1", "8443"); got != "https://[fe80::1]:8443" {
		t.Errorf("IPv6 literal must be bracketed, got %q", got)
	}
}

func TestHTTPSAddrPort(t *testing.T) {
	if got := protocol.HTTPSAddrPort("10.0.0.1", 8080); got != "https://10.0.0.1:8080" {
		t.Errorf("unexpected address %q", got)
	}
	if got := protocol.HTTPSAddrPort("fe80::1", 443); got != "https://[fe80::1]:443" {
		t.Errorf("IPv6 literal must be bracketed, got %q", got)
	}
}
