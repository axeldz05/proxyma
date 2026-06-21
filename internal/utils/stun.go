package utils

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// IsPrivateOrCGNATIP returns true if the IP is in private, loopback, or CGNAT IP ranges.
func IsPrivateOrCGNATIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		// For the scope of this project, we prioritize IPv4. If it's IPv6, return false (or check IPv6 local/private if needed).
		return false
	}

	// Loopback: 127.0.0.0/8
	if ipv4[0] == 127 {
		return true
	}

	// RFC 1918 Private Ranges:
	// 10.0.0.0/8
	if ipv4[0] == 10 {
		return true
	}
	// 172.16.0.0/12 (172.16.0.0 to 172.31.255.255)
	if ipv4[0] == 172 && ipv4[1] >= 16 && ipv4[1] <= 31 {
		return true
	}
	// 192.168.0.0/16
	if ipv4[0] == 192 && ipv4[1] == 168 {
		return true
	}

	// RFC 6598 CGNAT Range: 100.64.0.0/10 (100.64.0.0 to 100.127.255.255)
	if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
		return true
	}

	return false
}

// GetExternalIPPort queries the STUN server to discover the external/public IP and port.
func GetExternalIPPort(stunServer string, timeout time.Duration) (string, int, error) {
	addr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", 0, fmt.Errorf("failed to resolve STUN address: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return "", 0, fmt.Errorf("failed to connect to STUN: %w", err)
	}
	defer conn.Close()

	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	// Construct STUN Request header (20 bytes)
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], 0x0001)  // Binding Request
	binary.BigEndian.PutUint16(req[2:4], 0x0000)  // Message Length (0 attributes)
	binary.BigEndian.PutUint32(req[4:8], 0x2112A442) // Magic Cookie

	txID := req[8:20]
	if _, err := rand.Read(txID); err != nil {
		return "", 0, fmt.Errorf("failed to generate random transaction ID: %w", err)
	}

	if _, err := conn.Write(req); err != nil {
		return "", 0, fmt.Errorf("failed to write STUN request: %w", err)
	}

	resp := make([]byte, 1024)
	n, err := conn.Read(resp)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read STUN response: %w", err)
	}

	if n < 20 {
		return "", 0, fmt.Errorf("STUN response too short (%d bytes)", n)
	}

	msgType := binary.BigEndian.Uint16(resp[0:2])
	msgLen := binary.BigEndian.Uint16(resp[2:4])
	magicCookie := binary.BigEndian.Uint32(resp[4:8])

	if msgType != 0x0101 { // Success Response
		return "", 0, fmt.Errorf("unexpected STUN message type: 0x%04X", msgType)
	}
	if magicCookie != 0x2112A442 {
		return "", 0, fmt.Errorf("invalid STUN magic cookie")
	}

	// Compare transaction ID
	for i := 0; i < 12; i++ {
		if resp[8+i] != txID[i] {
			return "", 0, fmt.Errorf("mismatched transaction ID in STUN response")
		}
	}

	if int(msgLen)+20 > n {
		return "", 0, fmt.Errorf("STUN response message length mismatch (expected %d, read %d)", msgLen+20, n)
	}

	// Parse attributes
	attributes := resp[20 : 20+msgLen]
	idx := 0
	for idx < len(attributes) {
		if idx+4 > len(attributes) {
			break
		}
		attrType := binary.BigEndian.Uint16(attributes[idx : idx+2])
		attrLen := binary.BigEndian.Uint16(attributes[idx+2 : idx+4])
		
		paddedLen := (int(attrLen) + 3) &^ 3
		if idx+4+paddedLen > len(attributes) {
			break
		}

		attrVal := attributes[idx+4 : idx+4+int(attrLen)]

		// Parse MAPPED-ADDRESS (0x0001) or XOR-MAPPED-ADDRESS (0x0020)
		if attrType == 0x0001 { // MAPPED-ADDRESS
			if len(attrVal) >= 8 {
				family := attrVal[1]
				if family == 1 { // IPv4
					port := binary.BigEndian.Uint16(attrVal[2:4])
					ip := net.IP(attrVal[4:8])
					return ip.String(), int(port), nil
				}
			}
		} else if attrType == 0x0020 { // XOR-MAPPED-ADDRESS
			if len(attrVal) >= 8 {
				family := attrVal[1]
				if family == 1 { // IPv4
					xPort := binary.BigEndian.Uint16(attrVal[2:4])
					port := xPort ^ 0x2112

					xIP := binary.BigEndian.Uint32(attrVal[4:8])
					ipVal := xIP ^ 0x2112A442
					ip := make(net.IP, 4)
					binary.BigEndian.PutUint32(ip, ipVal)

					return ip.String(), int(port), nil
				}
			}
		}

		idx += 4 + paddedLen
	}

	return "", 0, fmt.Errorf("no MAPPED-ADDRESS or XOR-MAPPED-ADDRESS found in STUN response")
}
