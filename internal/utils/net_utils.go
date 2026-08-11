package utils

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
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
