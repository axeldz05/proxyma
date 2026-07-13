package p2p

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/fd/go-nat"
	"github.com/stretchr/testify/assert"
)

type mockNAT struct {
	natType      string
	deviceAddr   net.IP
	externalAddr net.IP
	internalAddr net.IP
	mappings     map[string]int
	addCalls     int
	delCalls     int
}

func (m *mockNAT) Type() string {
	return m.natType
}

func (m *mockNAT) GetDeviceAddress() (net.IP, error) {
	return m.deviceAddr, nil
}

func (m *mockNAT) GetExternalAddress() (net.IP, error) {
	return m.externalAddr, nil
}

func (m *mockNAT) GetInternalAddress() (net.IP, error) {
	return m.internalAddr, nil
}

func (m *mockNAT) AddPortMapping(protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	m.addCalls++
	key := fmt.Sprintf("%s:%d", protocol, internalPort)
	extPort := internalPort + 100 // mapped differently to verify it's captured
	m.mappings[key] = extPort
	return extPort, nil
}

func (m *mockNAT) DeletePortMapping(protocol string, internalPort int) error {
	m.delCalls++
	key := fmt.Sprintf("%s:%d", protocol, internalPort)
	delete(m.mappings, key)
	return nil
}

func TestNATMapperLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tcpPort := 9000
	udpPort := 9001

	nm := NewNATMapper(logger, tcpPort, udpPort)
	assert.NotNil(t, nm)

	m := &mockNAT{
		natType:      "Mock NAT-PMP",
		deviceAddr:   net.ParseIP("192.168.1.1"),
		externalAddr: net.ParseIP("203.0.113.1"),
		internalAddr: net.ParseIP("192.168.1.10"),
		mappings:     make(map[string]int),
	}
	nm.discoverGateway = func() (nat.NAT, error) {
		return m, nil
	}

	// Start mapping process
	nm.Start()

	// Wait for mapping to happen on background goroutine
	assert.Eventually(t, func() bool {
		tcpExt, udpExt := nm.GetMappedPorts()
		return tcpExt == 9100 && udpExt == 9101
	}, 1*time.Second, 10*time.Millisecond)

	// Verify GetExternalAddress
	extIP, err := nm.GetExternalAddress()
	assert.NoError(t, err)
	assert.Equal(t, "203.0.113.1", extIP.String())

	// Test Stop cleans up
	nm.Stop()

	// Verify deletions were executed
	assert.Eventually(t, func() bool {
		return m.delCalls == 2
	}, 1*time.Second, 10*time.Millisecond)

	assert.Empty(t, m.mappings)
}
