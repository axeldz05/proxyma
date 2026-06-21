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

func TestSTUNClient(t *testing.T) {
	// Start a local mock STUN server
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer conn.Close()

	addr := conn.LocalAddr().String()

	// Run mock STUN responder in goroutine
	go func() {
		buf := make([]byte, 1024)
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 20 {
			return
		}

		// Verify Magic Cookie
		magicCookie := binary.BigEndian.Uint32(buf[4:8])
		if magicCookie != 0x2112A442 {
			return
		}

		// Transaction ID
		txID := buf[8:20]

		// Construct response with XOR-MAPPED-ADDRESS
		resp := make([]byte, 32)
		// Header (20 bytes)
		binary.BigEndian.PutUint16(resp[0:2], 0x0101)  // Success Response
		binary.BigEndian.PutUint16(resp[2:4], 12)      // Length of attributes (12 bytes: 4 bytes attr header + 8 bytes value)
		binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)
		copy(resp[8:20], txID)

		// Attribute: XOR-MAPPED-ADDRESS (12 bytes)
		binary.BigEndian.PutUint16(resp[20:22], 0x0020) // Type
		binary.BigEndian.PutUint16(resp[22:24], 8)      // Length
		resp[24] = 0x00                                 // Reserved
		resp[25] = 0x01                                 // IPv4 Family

		// Sender IP and port
		senderIP := raddr.IP.To4()
		senderPort := uint16(raddr.Port)

		// XOR Port: port ^ (MagicCookie >> 16) = port ^ 0x2112
		xPort := senderPort ^ 0x2112
		binary.BigEndian.PutUint16(resp[26:28], xPort)

		// XOR Address: IP ^ MagicCookie = IP ^ 0x2112A442
		xAddress := binary.BigEndian.Uint32(senderIP) ^ 0x2112A442
		binary.BigEndian.PutUint32(resp[28:32], xAddress)

		_, _ = conn.WriteToUDP(resp[:32], raddr)
	}()

	// Query STUN
	extIP, extPort, err := GetExternalIPPort(addr, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", extIP)
	require.True(t, extPort > 0)
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
