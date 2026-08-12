package utils

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const stunMagicCookie uint32 = 0x2112A442

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
	if n > len(resp) {
		return "", 0, fmt.Errorf("STUN response length %d exceeds buffer size %d", n, len(resp))
	}
	if n < 20 {
		return "", 0, fmt.Errorf("STUN response too short (%d bytes)", n)
	}
	if len(txID) != 12 {
		return "", 0, fmt.Errorf("invalid STUN transaction ID length: %d", len(txID))
	}

	msgType := binary.BigEndian.Uint16(resp[0:2])
	msgLen := int(binary.BigEndian.Uint16(resp[2:4]))
	magicCookie := binary.BigEndian.Uint32(resp[4:8])

	if msgType != 0x0101 {
		return "", 0, fmt.Errorf("unexpected STUN message type: 0x%04X", msgType)
	}
	if magicCookie != stunMagicCookie {
		return "", 0, fmt.Errorf("invalid STUN magic cookie")
	}

	for i := range 12 {
		if resp[8+i] != txID[i] {
			return "", 0, fmt.Errorf("mismatched transaction ID in STUN response")
		}
	}

	if msgLen+20 > n {
		return "", 0, fmt.Errorf("STUN response message length mismatch (expected %d, read %d)", msgLen+20, n)
	}

	attributes := resp[20 : 20+msgLen]
	idx := 0
	for idx < len(attributes) {
		if idx+4 > len(attributes) {
			return "", 0, fmt.Errorf("truncated STUN attribute header")
		}
		attrType := binary.BigEndian.Uint16(attributes[idx : idx+2])
		attrLen := int(binary.BigEndian.Uint16(attributes[idx+2 : idx+4]))

		paddedLen := (attrLen + 3) &^ 3
		if idx+4+paddedLen > len(attributes) {
			return "", 0, fmt.Errorf("truncated STUN attribute 0x%04X", attrType)
		}

		attrVal := attributes[idx+4 : idx+4+attrLen]

		switch attrType {
		case 0x0001: // MAPPED-ADDRESS
			return parseSTUNMappedAddress(attrVal, txID, false)
		case 0x0020: // XOR-MAPPED-ADDRESS
			return parseSTUNMappedAddress(attrVal, txID, true)
		}

		idx += 4 + paddedLen
	}

	return "", 0, fmt.Errorf("no MAPPED-ADDRESS or XOR-MAPPED-ADDRESS found in STUN response")
}

func parseSTUNMappedAddress(attrVal, txID []byte, xor bool) (string, int, error) {
	if len(attrVal) < 4 {
		return "", 0, fmt.Errorf("mapped-address STUN attribute is too short")
	}

	port := binary.BigEndian.Uint16(attrVal[2:4])
	if xor {
		port ^= uint16(stunMagicCookie >> 16)
	}

	var addressLength int
	switch attrVal[1] {
	case 0x01:
		addressLength = net.IPv4len
	case 0x02:
		addressLength = net.IPv6len
	default:
		return "", 0, fmt.Errorf("unsupported STUN mapped-address family: 0x%02X", attrVal[1])
	}
	if len(attrVal) < 4+addressLength {
		return "", 0, fmt.Errorf("truncated STUN mapped-address for family 0x%02X", attrVal[1])
	}

	ip := append(net.IP(nil), attrVal[4:4+addressLength]...)
	if xor {
		mask := make([]byte, addressLength)
		binary.BigEndian.PutUint32(mask[:4], stunMagicCookie)
		if addressLength == net.IPv6len {
			if len(txID) != 12 {
				return "", 0, fmt.Errorf("invalid STUN transaction ID length: %d", len(txID))
			}
			copy(mask[4:], txID)
		}
		for i := range ip {
			ip[i] ^= mask[i]
		}
	}
	return ip.String(), int(port), nil
}

// buildSTUNRequest creates a 20-byte STUN Binding Request with a random transaction ID.
func buildSTUNRequest() ([]byte, []byte, error) {
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], 0x0001)          // Binding Request
	binary.BigEndian.PutUint16(req[2:4], 0x0000)          // Message Length (0 attributes)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie) // Magic Cookie

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

// GetExternalUDPListener binds a local UDP socket, queries the STUN server,
// and returns the public IP/port along with the active *net.UDPConn (unconnected)
// so it can be reused for UDP Hole Punching.
func GetExternalUDPListener(stunServer string, timeout time.Duration) (string, int, *net.UDPConn, error) {
	return GetExternalUDPListenerContext(context.Background(), stunServer, timeout)
}

// GetExternalUDPListenerContext is GetExternalUDPListener with cancellation.
// The socket family follows the resolved STUN endpoint so IPv4 and IPv6 are
// never mixed on a family-specific socket.
func GetExternalUDPListenerContext(ctx context.Context, stunServer string, timeout time.Duration) (string, int, *net.UDPConn, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	addr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to resolve STUN address: %w", err)
	}
	if addr.IP == nil {
		return "", 0, nil, fmt.Errorf("STUN address %q did not resolve to an IP", stunServer)
	}

	network := "udp4"
	bindIP := net.IPv4zero
	if addr.IP.To4() == nil {
		if addr.IP.To16() == nil {
			return "", 0, nil, fmt.Errorf("STUN address %q has an invalid IP family", stunServer)
		}
		network = "udp6"
		bindIP = net.IPv6unspecified
	}

	// Bind to an ephemeral UDP port in the STUN endpoint's address family.
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: bindIP, Port: 0})
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to bind local UDP port: %w", err)
	}

	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()

	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-stopWatch:
		}
	}()
	watching := true
	stopCancellationWatch := func() {
		if !watching {
			return
		}
		close(stopWatch)
		<-watchDone
		watching = false
	}
	defer stopCancellationWatch()

	ip, port, err := stunExchange(conn.SetDeadline,
		func(b []byte) error { _, err := conn.WriteTo(b, addr); return err },
		func(b []byte) (int, error) {
			n, _, err := conn.ReadFrom(b)
			return n, err
		},
		timeout,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", 0, nil, ctxErr
		}
		return "", 0, nil, err
	}

	stopCancellationWatch()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", 0, nil, ctxErr
	}
	// Clear deadline so the socket can be reused
	_ = conn.SetDeadline(time.Time{})

	success = true
	return ip, port, conn, nil
}
