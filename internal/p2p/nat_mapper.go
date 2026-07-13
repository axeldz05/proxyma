package p2p

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/fd/go-nat"
)

type NATMapper struct {
	logger        *slog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	tcpPort       int
	udpPort       int
	tcpMappedPort int
	udpMappedPort int
	natDev        nat.NAT
}

func NewNATMapper(logger *slog.Logger, tcpPort, udpPort int) *NATMapper {
	return &NATMapper{
		logger:  logger,
		tcpPort: tcpPort,
		udpPort: udpPort,
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
	dev, err := nat.DiscoverGateway()
	if err != nil {
		if nm.logger != nil {
			nm.logger.Warn("Failed to discover NAT gateway (UPnP/NAT-PMP might be disabled on your router)", "error", err)
		}
		return
	}
	nm.natDev = dev

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
	if nm.natDev == nil {
		return
	}

	if nm.tcpPort > 0 {
		extPort, err := nm.natDev.AddPortMapping("tcp", nm.tcpPort, "proxyma-tcp", 30*time.Minute)
		if err != nil {
			if nm.logger != nil {
				nm.logger.Warn("Failed to map TCP port", "internalPort", nm.tcpPort, "error", err)
			}
		} else {
			nm.tcpMappedPort = extPort
			if nm.logger != nil {
				nm.logger.Info("TCP port mapped successfully", "internal", nm.tcpPort, "external", extPort)
			}
		}
	}

	if nm.udpPort > 0 {
		extPort, err := nm.natDev.AddPortMapping("udp", nm.udpPort, "proxyma-udp", 30*time.Minute)
		if err != nil {
			if nm.logger != nil {
				nm.logger.Warn("Failed to map UDP port", "internalPort", nm.udpPort, "error", err)
			}
		} else {
			nm.udpMappedPort = extPort
			if nm.logger != nil {
				nm.logger.Info("UDP port mapped successfully", "internal", nm.udpPort, "external", extPort)
			}
		}
	}
}

func (nm *NATMapper) Stop() {
	if nm.cancel != nil {
		nm.cancel()
	}

	if nm.natDev == nil {
		return
	}

	if nm.tcpMappedPort > 0 {
		if nm.logger != nil {
			nm.logger.Info("Removing TCP port mapping", "port", nm.tcpPort)
		}
		_ = nm.natDev.DeletePortMapping("tcp", nm.tcpPort)
	}

	if nm.udpMappedPort > 0 {
		if nm.logger != nil {
			nm.logger.Info("Removing UDP port mapping", "port", nm.udpPort)
		}
		_ = nm.natDev.DeletePortMapping("udp", nm.udpPort)
	}
}

func (nm *NATMapper) GetMappedPorts() (int, int) {
	return nm.tcpMappedPort, nm.udpMappedPort
}

func (nm *NATMapper) GetExternalAddress() (net.IP, error) {
	if nm.natDev == nil {
		return nil, nat.ErrNoNATFound
	}
	return nm.natDev.GetExternalAddress()
}
