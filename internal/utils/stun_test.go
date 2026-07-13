package utils

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsPrivateOrCGNATIP(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.5", true},
		{"172.16.20.1", true},
		{"192.168.1.100", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"100.63.255.255", false}, // just outside CGNAT
		{"100.128.0.0", false},      // just outside CGNAT
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"invalid-ip", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := IsPrivateOrCGNATIP(tt.ip)
			require.Equal(t, tt.expected, result)
		})
	}
}

// mockSTUNResponder runs a goroutine that handles one STUN binding request
// and replies with an XOR-MAPPED-ADDRESS reflecting the sender's IP/port.
func mockSTUNResponder(conn *net.UDPConn) {
	buf := make([]byte, 1024)
	n, raddr, err := conn.ReadFromUDP(buf)
	if err != nil || n < 20 {
		return
	}

	if binary.BigEndian.Uint32(buf[4:8]) != 0x2112A442 {
		return
	}

	resp := make([]byte, 32)
	binary.BigEndian.PutUint16(resp[0:2], 0x0101)     // Success Response
	binary.BigEndian.PutUint16(resp[2:4], 12)          // Attribute length (4 header + 8 value)
	binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)  // Magic Cookie
	copy(resp[8:20], buf[8:20])                        // Transaction ID

	// XOR-MAPPED-ADDRESS attribute
	binary.BigEndian.PutUint16(resp[20:22], 0x0020) // Type
	binary.BigEndian.PutUint16(resp[22:24], 8)      // Length
	resp[24] = 0x00                                 // Reserved
	resp[25] = 0x01                                 // IPv4 Family

	senderIP := raddr.IP.To4()
	binary.BigEndian.PutUint16(resp[26:28], uint16(raddr.Port)^0x2112)
	binary.BigEndian.PutUint32(resp[28:32], binary.BigEndian.Uint32(senderIP)^0x2112A442)

	_, _ = conn.WriteToUDP(resp, raddr)
}

func TestSTUNClient(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	go mockSTUNResponder(conn)

	extIP, extPort, err := GetExternalIPPort(conn.LocalAddr().String(), 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", extIP)
	require.True(t, extPort > 0)
}

func TestGetExternalUDPListener(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	go mockSTUNResponder(conn)

	extIP, extPort, uconn, err := GetExternalUDPListener(conn.LocalAddr().String(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = uconn.Close() }()

	require.Equal(t, "127.0.0.1", extIP)
	require.True(t, extPort > 0)
	require.NotNil(t, uconn)
}

func TestExtractPort(t *testing.T) {
	tests := []struct {
		address  string
		expected string
	}{
		{"https://127.0.0.1:8443", "8443"},
		{"http://localhost:8080/manifest", "8080"},
		{"10.0.0.5:9000", "9000"},
		{"localhost", ""},
		{":3000", "3000"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			require.Equal(t, tt.expected, ExtractPort(tt.address))
		})
	}
}

