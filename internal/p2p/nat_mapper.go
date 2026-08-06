package p2p

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fd/go-nat"
)

type NATMapper struct {
	mu              sync.RWMutex
	logger          *slog.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	tcpPort         int
	udpPort         int
	tcpMappedPort   int
	udpMappedPort   int
	natDev          nat.NAT
	discoverGateway func() (nat.NAT, error)
	onMapped        func(mappedTCP, mappedUDP int)
}

func NewNATMapper(logger *slog.Logger, tcpPort, udpPort int) *NATMapper {
	return &NATMapper{
		logger:          logger,
		tcpPort:         tcpPort,
		udpPort:         udpPort,
		discoverGateway: nat.DiscoverGateway,
	}
}

// SetOnMapped registers a callback invoked after successful port mapping updates.
func (nm *NATMapper) SetOnMapped(fn func(mappedTCP, mappedUDP int)) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.onMapped = fn
}

func (nm *NATMapper) Start() {
	if nm.logger != nil {
		nm.logger.Info("Starting UPnP/NAT-PMP port mapping...")
	}
	ctx, cancel := context.WithCancel(context.Background())
	nm.ctx = ctx
	nm.cancel = cancel

	go nm.runMapper()
}

func (nm *NATMapper) runMapper() {
	dev, err := nm.discoverGateway()
	if err != nil {
		if nm.logger != nil {
			nm.logger.Warn("Failed to discover NAT gateway (UPnP/NAT-PMP might be disabled on your router)", "error", err)
		}
		return
	}

	nm.mu.Lock()
	nm.natDev = dev
	nm.mu.Unlock()

	if nm.logger != nil {
		nm.logger.Info("NAT gateway discovered", "type", dev.Type())
	}

	nm.refreshMappings()

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-nm.ctx.Done():
			return
		case <-ticker.C:
			nm.refreshMappings()
		}
	}
}

func (nm *NATMapper) refreshMappings() {
	nm.mu.RLock()
	dev := nm.natDev
	tcpPort := nm.tcpPort
	udpPort := nm.udpPort
	nm.mu.RUnlock()

	if dev == nil {
		return
	}

	if tcpPort > 0 {
		nm.mapPort(dev, "tcp", tcpPort, "proxyma-tcp", func(ext int) { nm.tcpMappedPort = ext })
	}
	if udpPort > 0 {
		nm.mapPort(dev, "udp", udpPort, "proxyma-udp", func(ext int) { nm.udpMappedPort = ext })
	}
}

func (nm *NATMapper) mapPort(dev nat.NAT, proto string, port int, desc string, setMapped func(int)) {
	extPort, err := dev.AddPortMapping(proto, port, desc, 30*time.Minute)
	if err != nil {
		if nm.logger != nil {
			nm.logger.Warn("Failed to map "+strings.ToUpper(proto)+" port", "internalPort", port, "error", err)
		}
		return
	}
	nm.mu.Lock()
	setMapped(extPort)
	cb := nm.onMapped
	tcpMapped, udpMapped := nm.tcpMappedPort, nm.udpMappedPort
	nm.mu.Unlock()
	if nm.logger != nil {
		nm.logger.Info(strings.ToUpper(proto)+" port mapped successfully", "internal", port, "external", extPort)
	}
	if cb != nil {
		cb(tcpMapped, udpMapped)
	}
}

func (nm *NATMapper) Stop() {
	if nm.cancel != nil {
		nm.cancel()
	}

	nm.mu.Lock()
	dev := nm.natDev
	tcpPort, tcpMapped := nm.tcpPort, nm.tcpMappedPort
	udpPort, udpMapped := nm.udpPort, nm.udpMappedPort
	nm.mu.Unlock()

	if dev == nil {
		return
	}

	if tcpMapped > 0 {
		nm.unmapPort(dev, "tcp", tcpPort)
	}
	if udpMapped > 0 {
		nm.unmapPort(dev, "udp", udpPort)
	}
}

func (nm *NATMapper) unmapPort(dev nat.NAT, proto string, port int) {
	if nm.logger != nil {
		nm.logger.Info("Removing "+strings.ToUpper(proto)+" port mapping", "port", port)
	}
	_ = dev.DeletePortMapping(proto, port)
}

func (nm *NATMapper) GetMappedPorts() (int, int) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.tcpMappedPort, nm.udpMappedPort
}

func (nm *NATMapper) GetExternalAddress() (net.IP, error) {
	nm.mu.RLock()
	dev := nm.natDev
	nm.mu.RUnlock()

	if dev == nil {
		return nil, nat.ErrNoNATFound
	}
	return dev.GetExternalAddress()
}
