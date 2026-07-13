package p2p

import (
	"context"
	"log/slog"
	"net"
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
}

func NewNATMapper(logger *slog.Logger, tcpPort, udpPort int) *NATMapper {
	return &NATMapper{
		logger:          logger,
		tcpPort:         tcpPort,
		udpPort:         udpPort,
		discoverGateway: nat.DiscoverGateway,
	}
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
		extPort, err := dev.AddPortMapping("tcp", tcpPort, "proxyma-tcp", 30*time.Minute)
		if err != nil {
			if nm.logger != nil {
				nm.logger.Warn("Failed to map TCP port", "internalPort", tcpPort, "error", err)
			}
		} else {
			nm.mu.Lock()
			nm.tcpMappedPort = extPort
			nm.mu.Unlock()
			if nm.logger != nil {
				nm.logger.Info("TCP port mapped successfully", "internal", tcpPort, "external", extPort)
			}
		}
	}

	if udpPort > 0 {
		extPort, err := dev.AddPortMapping("udp", udpPort, "proxyma-udp", 30*time.Minute)
		if err != nil {
			if nm.logger != nil {
				nm.logger.Warn("Failed to map UDP port", "internalPort", udpPort, "error", err)
			}
		} else {
			nm.mu.Lock()
			nm.udpMappedPort = extPort
			nm.mu.Unlock()
			if nm.logger != nil {
				nm.logger.Info("UDP port mapped successfully", "internal", udpPort, "external", extPort)
			}
		}
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
		if nm.logger != nil {
			nm.logger.Info("Removing TCP port mapping", "port", tcpPort)
		}
		_ = dev.DeletePortMapping("tcp", tcpPort)
	}

	if udpMapped > 0 {
		if nm.logger != nil {
			nm.logger.Info("Removing UDP port mapping", "port", udpPort)
		}
		_ = dev.DeletePortMapping("udp", udpPort)
	}
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
