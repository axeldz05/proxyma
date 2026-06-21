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
