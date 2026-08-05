package utils

import (
	"net"
	"net/url"
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
