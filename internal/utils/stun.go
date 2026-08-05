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
	if ip.IsPrivate() || ip.IsLoopback() {
		return true
	}
	// RFC 6598 CGNAT 100.64.0.0/10 — not covered by net.IP.IsPrivate.
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127
}

// parseSTUNResponse validates a STUN response and extracts the mapped IP/port.
func parseSTUNResponse(resp []byte, n int, txID []byte) (string, int, error) {
	if n < 20 {
		return "", 0, fmt.Errorf("STUN response too short (%d bytes)", n)
	}

	msgType := binary.BigEndian.Uint16(resp[0:2])
	msgLen := binary.BigEndian.Uint16(resp[2:4])
	magicCookie := binary.BigEndian.Uint32(resp[4:8])

	if msgType != 0x0101 {
		return "", 0, fmt.Errorf("unexpected STUN message type: 0x%04X", msgType)
	}
	if magicCookie != 0x2112A442 {
		return "", 0, fmt.Errorf("invalid STUN magic cookie")
	}

	for i := range 12 {
		if resp[8+i] != txID[i] {
			return "", 0, fmt.Errorf("mismatched transaction ID in STUN response")
		}
	}

	if int(msgLen)+20 > n {
		return "", 0, fmt.Errorf("STUN response message length mismatch (expected %d, read %d)", msgLen+20, n)
	}

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

		switch attrType {
		case 0x0001: // MAPPED-ADDRESS
			if len(attrVal) >= 8 && attrVal[1] == 1 { // IPv4
				port := binary.BigEndian.Uint16(attrVal[2:4])
				ip := net.IP(attrVal[4:8])
				return ip.String(), int(port), nil
			}
		case 0x0020: // XOR-MAPPED-ADDRESS
			if len(attrVal) >= 8 && attrVal[1] == 1 { // IPv4
				port := binary.BigEndian.Uint16(attrVal[2:4]) ^ 0x2112
				ipVal := binary.BigEndian.Uint32(attrVal[4:8]) ^ 0x2112A442
				ip := make(net.IP, 4)
				binary.BigEndian.PutUint32(ip, ipVal)
				return ip.String(), int(port), nil
			}
		}

		idx += 4 + paddedLen
	}

	return "", 0, fmt.Errorf("no MAPPED-ADDRESS or XOR-MAPPED-ADDRESS found in STUN response")
}

// buildSTUNRequest creates a 20-byte STUN Binding Request with a random transaction ID.
func buildSTUNRequest() ([]byte, []byte, error) {
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], 0x0001)     // Binding Request
	binary.BigEndian.PutUint16(req[2:4], 0x0000)     // Message Length (0 attributes)
	binary.BigEndian.PutUint32(req[4:8], 0x2112A442) // Magic Cookie

	txID := req[8:20]
	if _, err := rand.Read(txID); err != nil {
		return nil, nil, fmt.Errorf("failed to generate random transaction ID: %w", err)
	}
	return req, txID, nil
}

func stunExchange(setDeadline func(time.Time) error, write func([]byte) error, read func([]byte) (int, error), timeout time.Duration) (string, int, error) {
	if timeout > 0 {
		_ = setDeadline(time.Now().Add(timeout))
	}
	req, txID, err := buildSTUNRequest()
	if err != nil {
		return "", 0, err
	}
	if err := write(req); err != nil {
		return "", 0, fmt.Errorf("failed to write STUN request: %w", err)
	}
	resp := make([]byte, 1024)
	n, err := read(resp)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read STUN response: %w", err)
	}
	return parseSTUNResponse(resp, n, txID)
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
	defer func() { _ = conn.Close() }()

	return stunExchange(conn.SetDeadline,
		func(b []byte) error { _, err := conn.Write(b); return err },
		func(b []byte) (int, error) { return conn.Read(b) },
		timeout,
	)
}

// GetExternalUDPListener binds a local UDP socket, queries the STUN server,
// and returns the public IP/port along with the active *net.UDPConn (unconnected)
// so it can be reused for UDP Hole Punching.
func GetExternalUDPListener(stunServer string, timeout time.Duration) (string, int, *net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to resolve STUN address: %w", err)
	}

	// Bind to an ephemeral UDP port locally
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to bind local UDP port: %w", err)
	}

	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()

	ip, port, err := stunExchange(conn.SetDeadline,
		func(b []byte) error { _, err := conn.WriteTo(b, addr); return err },
		func(b []byte) (int, error) {
			n, _, err := conn.ReadFrom(b)
			return n, err
		},
		timeout,
	)
	if err != nil {
		return "", 0, nil, err
	}

	// Clear deadline so the socket can be reused
	_ = conn.SetDeadline(time.Time{})

	success = true
	return ip, port, conn, nil
}
