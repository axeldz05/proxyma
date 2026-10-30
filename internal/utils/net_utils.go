package utils

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// GetLocalIPs returns all non-loopback IP addresses of the system.
func GetLocalIPs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			ips = append(ips, ipnet.IP)
		}
	}
	return ips, nil
}

// GetRoutableLocalIPs returns non-loopback, non-link-local IPs (L2).
func GetRoutableLocalIPs() ([]net.IP, error) {
	ips, err := GetLocalIPs()
	if err != nil {
		return nil, err
	}
	var filtered []net.IP
	for _, ip := range ips {
		if ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
			continue
		}
		filtered = append(filtered, ip)
	}
	return filtered, nil
}

// StripURLScheme removes http:// or https:// prefix from addr (L1).
func StripURLScheme(addr string) string {
	addr = strings.TrimPrefix(addr, "https://")
	return strings.TrimPrefix(addr, "http://")
}

// FileExists reports whether path exists and is not a directory.
func FileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// ClientHost extracts the host from a RemoteAddr (host:port or bare host) (L1).
func ClientHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

// HTTPSuccess reports whether status is 2xx.
func HTTPSuccess(code int) bool {
	return code >= 200 && code < 300
}

// HTTPStatusError describes an unexpected status code (L1 SSOT for the message).
func HTTPStatusError(code int) error {
	return fmt.Errorf("unexpected status code: %d", code)
}

// HTTPErrorFromResponse returns nil for 2xx, otherwise an error carrying the
// status and the (already consumed) response body (L1). Callers keep owning Close.
func HTTPErrorFromResponse(resp *http.Response, label string) error {
	if HTTPSuccess(resp.StatusCode) {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%s returned status %d: %s", label, resp.StatusCode, string(body))
}

// ExtractPort parses the port from an address string (e.g. "https://127.0.0.1:8443" or "localhost:8080").
func ExtractPort(address string) string {
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err == nil {
			if strings.Contains(u.Host, ":") {
				_, port, err := net.SplitHostPort(u.Host)
				if err == nil {
					return port
				}
			}
		}
	} else if strings.Contains(address, ":") {
		_, port, err := net.SplitHostPort(address)
		if err == nil {
			return port
		}
	}

	// Fallback/split by colon
	parts := strings.Split(address, ":")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return ""
}

// IsLoopbackHost reports whether addr's host is localhost/loopback or a bare hostname without dots.
// Accepts full URLs, host:port, or bare hostnames.
func IsLoopbackHost(addr string) bool {
	host := addr
	if strings.Contains(addr, "://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return true
		}
		host = parsed.Hostname()
	} else if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return true
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || !strings.Contains(host, ".")
}

// SameIPFamily reports whether ip belongs to network's IP family ("udp4"/"tcp4" vs "udp6"/"tcp6").
func SameIPFamily(ip net.IP, network string) bool {
	if strings.HasSuffix(network, "6") {
		return ip.To4() == nil
	}
	return ip.To4() != nil
}

var netResolver atomic.Pointer[net.Resolver]

func init() {
	netResolver.Store(net.DefaultResolver)
}

func currentNetResolver() *net.Resolver {
	if r := netResolver.Load(); r != nil {
		return r
	}
	return net.DefaultResolver
}

// SetNetResolverForTest overrides the resolver used by ResolveUDPAddrContext (tests).
func SetNetResolverForTest(r *net.Resolver) func() {
	target := r
	if target == nil {
		target = net.DefaultResolver
	}
	prev := netResolver.Swap(target)
	return func() {
		if prev == nil {
			netResolver.Store(net.DefaultResolver)
		} else {
			netResolver.Store(prev)
		}
	}
}

// ResolveUDPAddrContext resolves a UDP address with context cancellation.
// When network is "udp", IPv4 is preferred if available.
func ResolveUDPAddrContext(ctx context.Context, network, address string) (*net.UDPAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("failed to parse UDP address: %w", err)
	}
	resolver := currentNetResolver()
	port, err := strconv.Atoi(portStr)
	if err != nil {
		port, err = resolver.LookupPort(ctx, network, portStr)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve UDP port: %w", err)
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if (network == "udp4" || network == "udp6") && !SameIPFamily(ip, network) {
			return nil, fmt.Errorf("address %q did not resolve to a compatible IP", address)
		}
		return &net.UDPAddr{IP: ip, Port: port}, nil
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve address: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("address %q did not resolve to an IP", address)
	}
	var selectedIP net.IP
	if network == "udp4" || network == "udp6" {
		for _, ip := range ips {
			if SameIPFamily(ip.IP, network) {
				selectedIP = ip.IP
				break
			}
		}
	} else {
		for _, ip := range ips {
			if SameIPFamily(ip.IP, "udp4") {
				selectedIP = ip.IP
				break
			}
		}
		if selectedIP == nil && len(ips) > 0 {
			selectedIP = ips[0].IP
		}
	}
	if selectedIP == nil {
		return nil, fmt.Errorf("address %q did not resolve to a compatible IP", address)
	}
	return &net.UDPAddr{IP: selectedIP, Port: port}, nil
}
