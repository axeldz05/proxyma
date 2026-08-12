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
	joinDiscovery   bool
	onMapped        func(mappedTCP, mappedUDP int)
	lifecycleMu     sync.Mutex
	started         bool
	stopRequested   bool
	wg              sync.WaitGroup
	cleanupOnce     sync.Once
	ready           chan struct{}
	readyOnce       sync.Once
	initialResult   NATMappingResult
}

type NATMappingResult struct {
	MappedTCP  int
	MappedUDP  int
	ExternalIP net.IP
	Err        error
}

func NewNATMapper(logger *slog.Logger, tcpPort, udpPort int) *NATMapper {
	// The lifetime is bound at construction so Stop is safe before Start and the
	// fields are never written once the mapper goroutine can observe them.
	ctx, cancel := context.WithCancel(context.Background())
	return &NATMapper{
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
		tcpPort:         tcpPort,
		udpPort:         udpPort,
		discoverGateway: nat.DiscoverGateway,
		ready:           make(chan struct{}),
	}
}

// stopped reports whether Stop already cancelled the mapper.
func (nm *NATMapper) stopped() bool {
	select {
	case <-nm.ctx.Done():
		return true
	default:
		return false
	}
}

// SetOnMapped registers a callback invoked after successful port mapping updates.
func (nm *NATMapper) SetOnMapped(fn func(mappedTCP, mappedUDP int)) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.onMapped = fn
}

// SetGatewayDiscovery replaces gateway discovery before Start. It is useful for
// deterministic embedders and tests that must not touch the host network.
func (nm *NATMapper) SetGatewayDiscovery(fn func() (nat.NAT, error)) {
	if fn == nil {
		return
	}
	nm.lifecycleMu.Lock()
	defer nm.lifecycleMu.Unlock()
	if !nm.started {
		nm.discoverGateway = fn
		nm.joinDiscovery = true
	}
}

func (nm *NATMapper) Start() {
	nm.lifecycleMu.Lock()
	defer nm.lifecycleMu.Unlock()
	if nm.started || nm.stopRequested {
		return
	}
	nm.started = true
	if nm.logger != nil {
		nm.logger.Info("Starting UPnP/NAT-PMP port mapping...")
	}
	nm.wg.Add(1)
	go func() {
		defer nm.wg.Done()
		nm.runMapper()
	}()
}

func (nm *NATMapper) runMapper() {
	backoff := time.Second
	const maxBackoff = 5 * time.Minute
	startupFailures := 0
	for {
		if nm.stopped() {
			return
		}
		dev, err := nm.discover()
		if nm.stopped() {
			nm.signalReady(NATMappingResult{Err: nm.ctx.Err()})
			return
		}
		if err != nil {
			startupFailures++
			retryIn := backoff
			if startupFailures == 1 {
				// One immediate bounded retry absorbs transient gateway startup
				// failures before readiness is published to the socket owner.
				retryIn = 10 * time.Millisecond
			} else {
				nm.signalReady(NATMappingResult{Err: err})
			}
			if nm.logger != nil {
				nm.logger.Warn("Failed to discover NAT gateway (UPnP/NAT-PMP might be disabled on your router); retrying",
					"error", err, "retryIn", retryIn)
			}
			select {
			case <-nm.ctx.Done():
				return
			case <-time.After(retryIn):
			}
			if startupFailures > 1 && backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}

		nm.mu.Lock()
		nm.natDev = dev
		nm.mu.Unlock()

		if nm.logger != nil {
			nm.logger.Info("NAT gateway discovered", "type", dev.Type())
		}

		nm.refreshMappings()
		mappedTCP, mappedUDP := nm.GetMappedPorts()
		externalIP, externalErr := nm.GetExternalAddress()
		nm.signalReady(NATMappingResult{
			MappedTCP:  mappedTCP,
			MappedUDP:  mappedUDP,
			ExternalIP: externalIP,
			Err:        externalErr,
		})

		ticker := time.NewTicker(15 * time.Minute)
		for {
			select {
			case <-nm.ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				nm.refreshMappings()
			}
		}
	}
}

type natDiscoveryResult struct {
	dev nat.NAT
	err error
}

func (nm *NATMapper) discover() (nat.NAT, error) {
	result := make(chan natDiscoveryResult, 1)
	if nm.joinDiscovery {
		nm.wg.Add(1)
	}
	go func() {
		if nm.joinDiscovery {
			defer nm.wg.Done()
		}
		dev, err := nm.discoverGateway()
		result <- natDiscoveryResult{dev: dev, err: err}
	}()
	select {
	case <-nm.ctx.Done():
		return nil, nm.ctx.Err()
	case discovered := <-result:
		return discovered.dev, discovered.err
	}
}

func (nm *NATMapper) signalReady(result NATMappingResult) {
	nm.readyOnce.Do(func() {
		nm.initialResult = result
		close(nm.ready)
	})
}

func (nm *NATMapper) WaitReady(ctx context.Context) (NATMappingResult, error) {
	select {
	case <-ctx.Done():
		return NATMappingResult{}, ctx.Err()
	case <-nm.ready:
		result := nm.initialResult
		return result, result.Err
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
		nm.mapPort(dev, "tcp", tcpPort, "proxyma-tcp", func(ext int) { nm.tcpMappedPort = ext }, func() { nm.tcpMappedPort = 0 })
	}
	if udpPort > 0 {
		nm.mapPort(dev, "udp", udpPort, "proxyma-udp", func(ext int) { nm.udpMappedPort = ext }, func() { nm.udpMappedPort = 0 })
	}
}

func (nm *NATMapper) mapPort(dev nat.NAT, proto string, port int, desc string, setMapped func(int), clearMapped func()) {
	extPort, err := dev.AddPortMapping(proto, port, desc, 30*time.Minute)
	// AddPortMapping talks to the router, so re-check the lifetime before reporting.
	if nm.stopped() {
		if err == nil {
			_ = dev.DeletePortMapping(proto, port)
		}
		return
	}
	if err != nil {
		if nm.logger != nil {
			nm.logger.Warn("Failed to map "+strings.ToUpper(proto)+" port", "internalPort", port, "error", err)
		}
		nm.mu.Lock()
		clearMapped()
		nm.mu.Unlock()
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
	nm.lifecycleMu.Lock()
	nm.stopRequested = true
	nm.cancel()
	nm.lifecycleMu.Unlock()
	nm.wg.Wait()
	nm.signalReady(NATMappingResult{Err: context.Canceled})
	nm.cleanupOnce.Do(nm.removeMappings)
}

func (nm *NATMapper) removeMappings() {
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
