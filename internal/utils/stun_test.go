package utils

import (
	"context"
	"encoding/binary"
	"errors"
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
		{"100.128.0.0", false},    // just outside CGNAT
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
	binary.BigEndian.PutUint16(resp[2:4], 12)         // Attribute length (4 header + 8 value)
	binary.BigEndian.PutUint32(resp[4:8], 0x2112A442) // Magic Cookie
	copy(resp[8:20], buf[8:20])                       // Transaction ID

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

func xorMappedIPv6Response(txID []byte, ip net.IP, port int) []byte {
	resp := make([]byte, 44)
	binary.BigEndian.PutUint16(resp[0:2], 0x0101)
	binary.BigEndian.PutUint16(resp[2:4], 24)
	binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)
	copy(resp[8:20], txID)
	binary.BigEndian.PutUint16(resp[20:22], 0x0020)
	binary.BigEndian.PutUint16(resp[22:24], 20)
	resp[25] = 0x02
	binary.BigEndian.PutUint16(resp[26:28], uint16(port)^0x2112)

	mask := make([]byte, net.IPv6len)
	binary.BigEndian.PutUint32(mask[:4], 0x2112A442)
	copy(mask[4:], txID)
	ip16 := ip.To16()
	for i := range net.IPv6len {
		resp[28+i] = ip16[i] ^ mask[i]
	}
	return resp
}

func TestParseSTUNXORMappedIPv6(t *testing.T) {
	t.Parallel()

	txID := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	wantIP := net.ParseIP("2001:db8:42::99")
	resp := xorMappedIPv6Response(txID, wantIP, 45678)

	ip, port, err := parseSTUNResponse(resp, len(resp), txID)
	require.NoError(t, err)
	require.Equal(t, wantIP.String(), ip)
	require.Equal(t, 45678, port)
}

func TestGetExternalUDPListenerIPv6(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	go func() {
		buf := make([]byte, 1024)
		n, remote, readErr := server.ReadFromUDP(buf)
		if readErr != nil || n < 20 {
			return
		}
		_, _ = server.WriteToUDP(xorMappedIPv6Response(buf[8:20], remote.IP, remote.Port), remote)
	}()

	ip, port, conn, err := GetExternalUDPListener(server.LocalAddr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.Equal(t, net.IPv6loopback.String(), ip)
	require.Positive(t, port)
	require.Equal(t, "udp", conn.LocalAddr().Network())
	require.Nil(t, conn.LocalAddr().(*net.UDPAddr).IP.To4())
}

func TestGetExternalUDPListenerContextCancellation(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	requestReceived := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		if _, _, readErr := server.ReadFromUDP(buf); readErr == nil {
			close(requestReceived)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, _, listenErr := GetExternalUDPListenerContext(ctx, server.LocalAddr().String(), time.Minute)
		result <- listenErr
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("STUN request was not sent")
	}
	cancel()

	select {
	case err := <-result:
		require.True(t, errors.Is(err, context.Canceled), "got %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceling STUN did not unblock the UDP read")
	}
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
