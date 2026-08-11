package server

import (
	"context"
	"fmt"
	"net"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strconv"
)

// configTCPPort returns the configured listen TCP port (default protocol.DefaultTCPPort) as string and int.
func (s *Server) configTCPPort() (portStr string, portInt int) {
	portStr = utils.ExtractPort(s.Config.Address)
	_, _ = fmt.Sscanf(protocol.DefaultTCPPort, "%d", &portInt)
	if portStr != "" {
		_, _ = fmt.Sscanf(portStr, "%d", &portInt)
	} else {
		portStr = protocol.DefaultTCPPort
	}
	return portStr, portInt
}

// advertisedTCPPort returns the public TCP port (UPnP mapped override when available).
func (s *Server) advertisedTCPPort() string {
	portStr, _ := s.configTCPPort()
	s.natMu.Lock()
	nm := s.natMapper
	s.natMu.Unlock()
	if nm != nil {
		if mappedTCP, _ := nm.GetMappedPorts(); mappedTCP > 0 {
			return fmt.Sprintf("%d", mappedTCP)
		}
	}
	return portStr
}

// defaultSTUNServer is used when the node config leaves STUNServer empty.
const defaultSTUNServer = "stun.l.google.com:19302"

// publicEndpoint is the externally visible UDP endpoint plus the socket that
// discovered it. The socket is later reused as the QUIC transport.
type publicEndpoint struct {
	IP   string
	Port int
	Conn *net.UDPConn
}

// applySponsorStatus records the auto-detected role, letting an explicit config
// override win (SSOT for every exit path of NAT detection).
func (s *Server) applySponsorStatus(detected bool) {
	if override := s.Config.IsSponsorOverride; override != nil {
		s.isSponsor = *override
		s.Config.Logger.Info("Sponsor status manually overridden", "isSponsor", s.isSponsor)
		return
	}
	s.isSponsor = detected
}

func (s *Server) determineSponsorAndNATStatus() {
	// Skip entirely if Shutdown already closed done.
	if s.shuttingDown() {
		return
	}
	s.Config.Logger.Info("Determining NAT and Sponsor status...")

	// An override only decides the sponsor role; NAT/QUIC setup still runs.
	endpoint, publicKnown, err := s.openUDPEndpoint()
	if err != nil {
		s.applySponsorStatus(false)
		return
	}

	// STUN blocked; drop the UDP socket if we shut down mid-probe.
	if s.shuttingDown() {
		_ = endpoint.Conn.Close()
		return
	}

	if !s.Config.DisableUPnP {
		endpoint, publicKnown = s.mapPortsWithUPnP(endpoint, publicKnown)
	}

	// UPnP (or the skip path) finished; do not start QUIC after shutdown.
	if s.shuttingDown() {
		_ = endpoint.Conn.Close()
		return
	}

	if !publicKnown {
		s.Config.Logger.Warn("Could not determine public IP, assuming private/CGNAT network")
		_ = endpoint.Conn.Close()
		s.applySponsorStatus(false)
		return
	}

	s.startDirectQUIC(endpoint)
	s.applySponsorStatus(s.detectPublicReachability(endpoint.IP))
}

// openUDPEndpoint returns the socket QUIC will use plus whether STUN already
// revealed a public address. A non-nil error means no socket could be opened.
func (s *Server) openUDPEndpoint() (publicEndpoint, bool, error) {
	stunServer := s.Config.STUNServer
	if stunServer == "" {
		stunServer = defaultSTUNServer
	}

	extIP, extPort, conn, err := utils.GetExternalUDPListener(stunServer, PeerRPCSTUN)
	if err == nil {
		s.Config.Logger.Debug("STUN public IP detected", "ip", extIP, "port", extPort)
		return publicEndpoint{IP: extIP, Port: extPort, Conn: conn}, true, nil
	}

	s.Config.Logger.Warn("STUN check failed, trying UPnP fallback to discover gateway", "error", err)
	if s.Config.DisableUPnP {
		return publicEndpoint{}, false, err
	}

	local, listenErr := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if listenErr != nil {
		s.Config.Logger.Warn("Could not bind local UDP socket after STUN failure", "error", listenErr)
		return publicEndpoint{}, false, listenErr
	}
	return publicEndpoint{Conn: local}, false, nil
}

// mapPortsWithUPnP starts the port mapper and, when STUN failed, recovers the
// public IP from the gateway. It reports whether a public IP is known afterwards.
func (s *Server) mapPortsWithUPnP(endpoint publicEndpoint, publicKnown bool) (publicEndpoint, bool) {
	// Avoid constructing a mapper if shutdown already won the race.
	if s.shuttingDown() {
		return endpoint, publicKnown
	}
	_, tcpPort := s.configTCPPort()
	udpPort := endpoint.Conn.LocalAddr().(*net.UDPAddr).Port

	mapper := p2p.NewNATMapper(s.Config.Logger, tcpPort, udpPort)
	mapper.SetOnMapped(func(mappedTCP, mappedUDP int) {
		s.refreshPublicUDPFromMapping(endpoint.IP, mappedUDP)
	})

	s.natMu.Lock()
	// Re-check under natMu so Shutdown cannot miss this mapper.
	if s.shuttingDown() {
		s.natMu.Unlock()
		mapper.Stop()
		return endpoint, publicKnown
	}
	s.natMapper = mapper
	s.natMu.Unlock()
	mapper.Start()

	mappedTCP, mappedUDP := mapper.GetMappedPorts()
	if mappedTCP > 0 {
		tcpPort = mappedTCP
	}
	if mappedUDP > 0 {
		endpoint.Port = mappedUDP
	}

	if publicKnown || mappedTCP <= 0 {
		return endpoint, publicKnown
	}

	routerIP, routerErr := mapper.GetExternalAddress()
	if routerErr != nil || routerIP == nil {
		return endpoint, false
	}
	endpoint.IP = routerIP.String()
	s.Config.Logger.Info("UPnP successfully mapped ports and retrieved public IP from gateway",
		"ip", endpoint.IP, "tcpPort", tcpPort, "udpPort", endpoint.Port)
	return endpoint, true
}

// startDirectQUIC reuses the discovery socket as the QUIC transport. Without TLS
// material there is nothing to serve, so the socket is released.
func (s *Server) startDirectQUIC(endpoint publicEndpoint) {
	s.tlsMutex.RLock()
	stls, ctls := s.serverTLSConfig, s.clientTLSConfig
	s.tlsMutex.RUnlock()

	if stls == nil || ctls == nil {
		_ = endpoint.Conn.Close()
		return
	}

	s.udpConn = endpoint.Conn
	s.publicUDPAddr = net.JoinHostPort(endpoint.IP, strconv.Itoa(endpoint.Port))
	s.quicMgr = p2p.NewQUICManager(s.Config.ID, endpoint.Conn, ctls, stls, s.handler, s.Config.Logger)
	s.quicMgr.PublicUDPAddr = s.publicUDPAddr

	if err := s.quicMgr.StartListener(); err != nil {
		s.Config.Logger.Error("Failed to start QUIC listener", "error", err)
		return
	}
	s.Config.Logger.Info("Direct QUIC listener started", "publicAddr", s.publicUDPAddr)
	s.peerClient.SetQUICManager(s.quicMgr)
}

// detectPublicReachability decides whether this node can serve as a Sponsor:
// CGNAT ranges never can, and a node with peers must be confirmed from outside.
func (s *Server) detectPublicReachability(extIP string) bool {
	if utils.IsPrivateOrCGNATIP(extIP) {
		s.Config.Logger.Info("Node is behind CGNAT/Private range. Auto-detected as NOT a Sponsor.", "ip", extIP)
		return false
	}

	if s.Config.BootstrapNode == "" {
		s.Config.Logger.Info("No Bootstrap Node configured. Assuming publicly reachable Sponsor since IP is public.", "ip", extIP)
		return true
	}

	s.Config.Logger.Info("Requesting reachability probe from Bootstrap Node...", "bootstrap", s.Config.BootstrapNode)
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCSTUN)
	defer cancel()

	probeResp, err := s.peerClient.RequestProbe(ctx, s.Config.BootstrapNode, protocol.ProbeRequest{
		Address: protocol.HTTPSAddr(extIP, s.advertisedTCPPort()),
	})
	switch {
	case err != nil:
		s.Config.Logger.Warn("Probe request to Bootstrap Node failed, assuming firewalled", "error", err)
		return false
	case !probeResp.Reachable:
		s.Config.Logger.Info("Node port is unreachable from outside (Firewalled). Auto-detected as NOT a Sponsor.", "error", probeResp.Error)
		return false
	default:
		s.Config.Logger.Info("Node is publicly reachable. Auto-detected as a Sponsor!")
		return true
	}
}

// refreshPublicUDPFromMapping updates publicUDPAddr when UPnP finishes mapping asynchronously.
func (s *Server) refreshPublicUDPFromMapping(extIP string, mappedUDP int) {
	if mappedUDP <= 0 || extIP == "" {
		return
	}
	host := extIP
	if s.publicUDPAddr != "" {
		if h, _, err := net.SplitHostPort(s.publicUDPAddr); err == nil && h != "" {
			host = h
		}
	}
	newAddr := fmt.Sprintf("%s:%d", host, mappedUDP)
	s.publicUDPAddr = newAddr
	if s.quicMgr != nil {
		s.quicMgr.PublicUDPAddr = newAddr
	}
	s.Config.Logger.Info("Updated public UDP address from NAT mapping", "publicUDP", newAddr)
}

func (s *Server) IsSponsorNode() bool {
	return s.isSponsor
}
