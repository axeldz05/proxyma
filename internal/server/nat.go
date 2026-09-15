package server

import (
	"context"
	"fmt"
	"net"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strconv"
	"time"
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
	s.natMu.RLock()
	nm := s.natMapper
	s.natMu.RUnlock()
	if nm != nil {
		if mappedTCP, _ := nm.GetMappedPorts(); mappedTCP > 0 {
			return fmt.Sprintf("%d", mappedTCP)
		}
	}
	return portStr
}

// defaultSTUNServer is used when the node config leaves STUNServer empty.
const defaultSTUNServer = "stun.l.google.com:19302"
const natSTUNAttempts = 2
const natMapperInitialWait = 250 * time.Millisecond

// publicEndpoint is the externally visible UDP endpoint plus the socket that
// discovered it. The socket is later reused as the QUIC transport.
type publicEndpoint struct {
	IP   string
	Port int
	Conn *net.UDPConn
}

// NATState is an atomic snapshot of the Server's published NAT/QUIC state.
type NATState struct {
	IsSponsor     bool
	PublicUDPAddr string
	QUICManager   *p2p.QUICManager
	TURNUDPAddr   string
}

// CurrentNATState returns a synchronized snapshot for cross-file consumers.
func (s *Server) CurrentNATState() NATState {
	s.natMu.RLock()
	defer s.natMu.RUnlock()
	state := NATState{
		IsSponsor:     s.isSponsor,
		PublicUDPAddr: s.publicUDPAddr,
		QUICManager:   s.quicMgr,
	}
	if s.turnMgr != nil {
		state.TURNUDPAddr = s.turnMgr.RelayedAddress()
	}
	return state
}

// detachTURNServer removes the embedded TURN server so shutdown closes it exactly once.
func (s *Server) detachTURNServer() *turnServer {
	s.natMu.Lock()
	defer s.natMu.Unlock()
	ts := s.turnSrv
	s.turnSrv = nil
	return ts
}

func (s *Server) detachQUICManager() *p2p.QUICManager {
	s.natMu.Lock()
	defer s.natMu.Unlock()
	qm := s.quicMgr
	s.quicMgr = nil
	s.udpConn = nil
	return qm
}

func (s *Server) beginNATWork() bool {
	s.natWorkMu.Lock()
	defer s.natWorkMu.Unlock()
	if s.lifetimeCtx.Err() != nil {
		return false
	}
	s.natWG.Add(1)
	return true
}

func (s *Server) runNATCheck() {
	// One owner performs the setup transaction. Transient STUN discovery is
	// retried inside openUDPEndpoint before any mapper or listener is published,
	// so retries cannot create duplicate long-lived network resources.
	s.checkNATOnce.Do(func() {
		check := s.natCheck
		if check == nil {
			check = s.determineSponsorAndNATStatus
		}
		check(s.lifetimeCtx)
	})
}

func (s *Server) scheduleNATCheck(delay time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if !s.beginNATWork() {
		close(done)
		return done
	}
	go func() {
		defer s.natWG.Done()
		defer close(done)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-s.lifetimeCtx.Done():
			return
		case <-timer.C:
			s.runNATCheck()
		}
	}()
	return done
}

func (s *Server) cancelServerLifetime() {
	s.natWorkMu.Lock()
	s.cancelLife()
	s.natWorkMu.Unlock()
}

func (s *Server) stopNATWork() {
	s.natWG.Wait()
	s.natMu.Lock()
	mapper := s.natMapper
	s.natMapper = nil
	s.natMu.Unlock()
	if mapper != nil {
		mapper.Stop()
	}
}

// applySponsorStatus records the auto-detected role, letting an explicit config
// override win (SSOT for every exit path of NAT detection).
func (s *Server) applySponsorStatus(detected bool) {
	s.natMu.Lock()
	if override := s.Config.IsSponsorOverride; override != nil {
		s.isSponsor = *override
		detected = s.isSponsor
		s.natMu.Unlock()
		s.Config.Logger.Info("Sponsor status manually overridden", "isSponsor", detected)
		return
	}
	s.isSponsor = detected
	s.natMu.Unlock()
}

func (s *Server) determineSponsorAndNATStatus(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.Config.Logger.Info("Determining NAT and Sponsor status...")

	// An override only decides the sponsor role; NAT/QUIC setup still runs.
	endpoint, publicKnown, err := s.openUDPEndpoint(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.applySponsorStatus(false)
		}
		return
	}

	// STUN blocked; drop the UDP socket if we shut down mid-probe.
	if ctx.Err() != nil {
		_ = endpoint.Conn.Close()
		return
	}

	if !s.Config.DisableUPnP {
		endpoint, publicKnown = s.mapPortsWithUPnP(ctx, endpoint, publicKnown)
	}

	// UPnP (or the skip path) finished; do not start QUIC after shutdown.
	if ctx.Err() != nil {
		_ = endpoint.Conn.Close()
		return
	}

	// The embedded TURN server needs the best-known public IP for relay
	// addresses; start it regardless of the sponsor outcome.
	s.maybeStartTURNServer(endpoint.IP)

	if !publicKnown {
		s.Config.Logger.Warn("Could not determine public IP, assuming private/CGNAT network")
		s.applySponsorStatus(false)
		// No announced endpoint, but the QUIC stack still runs: outbound
		// QUIC dials, TURN relays and inbound TURN sessions all work.
		s.startQUIC(ctx, endpoint, false)
		s.maybeStartTURNCallee()
		return
	}

	s.startQUIC(ctx, endpoint, true)
	if ctx.Err() != nil {
		return
	}
	detected := s.detectPublicReachability(ctx, endpoint.IP)
	if ctx.Err() == nil {
		s.applySponsorStatus(detected)
		if !detected {
			s.maybeStartTURNCallee()
		}
	}
}

// maybeStartTURNCallee allocates a TURN relayed address when the node concluded
// it is not publicly reachable, so peers can still dial it through the relay.
func (s *Server) maybeStartTURNCallee() {
	s.natMu.RLock()
	tm := s.turnMgr
	s.natMu.RUnlock()
	if tm == nil || s.lifetimeCtx.Err() != nil {
		return
	}
	s.goOwned(func() {
		if _, err := tm.EnsureAllocation(s.lifetimeCtx); err != nil {
			s.Config.Logger.Warn("TURN allocation failed; staying on direct+relay connectivity", "error", err)
		}
	})
}

// openUDPEndpoint returns the socket QUIC will use plus whether STUN already
// revealed a public address. A non-nil error means no socket could be opened.
func (s *Server) openUDPEndpoint(ctx context.Context) (publicEndpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return publicEndpoint{}, false, err
	}
	stunServer := s.Config.STUNServer
	if stunServer == "" {
		stunServer = defaultSTUNServer
	}

	var stunErr error
	stunTimeout := PeerRPCSTUN / natSTUNAttempts
	for attempt := 1; attempt <= natSTUNAttempts; attempt++ {
		extIP, extPort, conn, err := utils.GetExternalUDPListenerContext(ctx, stunServer, stunTimeout)
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = conn.Close()
				return publicEndpoint{}, false, ctxErr
			}
			s.Config.Logger.Debug("STUN public IP detected", "ip", extIP, "port", extPort, "attempt", attempt)
			return publicEndpoint{IP: extIP, Port: extPort, Conn: conn}, true, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return publicEndpoint{}, false, ctxErr
		}
		stunErr = err
		if attempt < natSTUNAttempts {
			s.Config.Logger.Warn("STUN check failed transiently; retrying",
				"error", err, "attempt", attempt, "maxAttempts", natSTUNAttempts)
		}
	}

	s.Config.Logger.Warn("STUN check failed, trying UPnP fallback to discover gateway", "error", stunErr)

	// Even without STUN the node keeps a UDP socket: outbound QUIC, TURN
	// relays and direct dials remain usable, only the announced public
	// endpoint is missing.
	local, listenErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if listenErr != nil {
		s.Config.Logger.Warn("Could not bind local UDP socket after STUN failure", "error", listenErr)
		return publicEndpoint{}, false, listenErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = local.Close()
		return publicEndpoint{}, false, ctxErr
	}
	return publicEndpoint{Conn: local}, false, nil
}

// mapPortsWithUPnP starts the port mapper and, when STUN failed, recovers the
// public IP from the gateway. It reports whether a public IP is known afterwards.
func (s *Server) mapPortsWithUPnP(ctx context.Context, endpoint publicEndpoint, publicKnown bool) (publicEndpoint, bool) {
	// Avoid constructing a mapper if shutdown already won the race.
	if ctx.Err() != nil {
		return endpoint, publicKnown
	}
	localUDP, ok := endpoint.Conn.LocalAddr().(*net.UDPAddr)
	if !ok || localUDP.IP.To4() == nil {
		s.Config.Logger.Debug("Skipping IPv4 NAT mapping for non-IPv4 UDP listener")
		return endpoint, publicKnown
	}
	if publicIP := net.ParseIP(endpoint.IP); publicIP != nil && publicIP.To4() == nil {
		s.Config.Logger.Debug("Skipping IPv4 NAT mapping for IPv6 public endpoint", "ip", endpoint.IP)
		return endpoint, publicKnown
	}

	_, tcpPort := s.configTCPPort()
	udpPort := localUDP.Port

	newMapper := s.natMapperFactory
	if newMapper == nil {
		newMapper = p2p.NewNATMapper
	}
	mapper := newMapper(s.Config.Logger, tcpPort, udpPort)

	s.natMu.Lock()
	// Re-check under natMu so Shutdown cannot miss this mapper.
	if ctx.Err() != nil {
		s.natMu.Unlock()
		mapper.Stop()
		return endpoint, publicKnown
	}
	s.natMapper = mapper
	s.natMu.Unlock()
	mapper.Start()

	waitCtx, cancelWait := context.WithTimeout(ctx, natMapperInitialWait)
	result, readyErr := mapper.WaitReady(waitCtx)
	cancelWait()
	if ctx.Err() != nil {
		s.discardNATMapper(mapper)
		return endpoint, publicKnown
	}
	if result.MappedTCP > 0 {
		tcpPort = result.MappedTCP
	}
	if result.MappedUDP > 0 {
		endpoint.Port = result.MappedUDP
	}
	if !publicKnown && result.ExternalIP != nil && result.MappedUDP > 0 {
		endpoint.IP = result.ExternalIP.String()
		publicKnown = true
	}

	if !publicKnown {
		s.Config.Logger.Warn("NAT mapper had no usable initial public endpoint", "error", readyErr)
		s.discardNATMapper(mapper)
		return endpoint, false
	}

	publicIP := endpoint.IP
	mapper.SetOnMapped(func(mappedTCP, mappedUDP int) {
		s.refreshPublicUDPFromMapping(publicIP, mappedUDP)
	})
	if readyErr != nil {
		s.Config.Logger.Warn("NAT mapper initial attempt failed; retaining STUN endpoint while mapper retries", "error", readyErr)
		return endpoint, true
	}
	s.Config.Logger.Info("UPnP successfully mapped ports and retrieved public IP from gateway",
		"ip", endpoint.IP, "tcpPort", tcpPort, "udpPort", endpoint.Port)
	return endpoint, true
}

func (s *Server) discardNATMapper(mapper *p2p.NATMapper) {
	s.natMu.Lock()
	if s.natMapper == mapper {
		s.natMapper = nil
	}
	s.natMu.Unlock()
	mapper.Stop()
}

// startDirectQUIC reuses the discovery socket as the QUIC transport with a
// known public endpoint (published for hole punching and announcements).
func (s *Server) startDirectQUIC(ctx context.Context, endpoint publicEndpoint) {
	s.startQUIC(ctx, endpoint, true)
}

// startQUIC reuses the discovery socket as the QUIC transport. With a known
// public endpoint its address is published; without one (STUN and UPnP both
// failed) the listener still serves outbound QUIC dials and TURN relays, it
// just cannot announce a direct quic:// address.
func (s *Server) startQUIC(ctx context.Context, endpoint publicEndpoint, publicKnown bool) {
	if ctx.Err() != nil {
		_ = endpoint.Conn.Close()
		return
	}
	if endpoint.Conn == nil {
		return
	}
	if publicKnown && (endpoint.IP == "" || endpoint.Port <= 0) {
		_ = endpoint.Conn.Close()
		s.Config.Logger.Error("Cannot start QUIC listener with an invalid public UDP endpoint",
			"ip", endpoint.IP, "port", endpoint.Port)
		return
	}
	s.tlsMutex.RLock()
	stls, ctls := s.serverTLSConfig, s.clientTLSConfig
	s.tlsMutex.RUnlock()

	if stls == nil || ctls == nil {
		_ = endpoint.Conn.Close()
		return
	}

	publicUDPAddr := ""
	if publicKnown {
		publicUDPAddr = net.JoinHostPort(endpoint.IP, strconv.Itoa(endpoint.Port))
	}
	qm := p2p.NewQUICManager(s.Config.ID, endpoint.Conn, ctls, stls, s.handler, s.Config.Logger)
	if publicUDPAddr != "" {
		qm.SetPublicUDPAddr(publicUDPAddr)
	}

	if err := qm.StartListener(); err != nil {
		s.Config.Logger.Error("Failed to start QUIC listener", "error", err)
		qm.Close()
		return
	}
	s.natMu.Lock()
	if ctx.Err() != nil || s.quicMgr != nil {
		s.natMu.Unlock()
		qm.Close()
		return
	}
	s.udpConn = endpoint.Conn
	if publicKnown {
		s.publicUDPAddr = publicUDPAddr
	}
	s.quicMgr = qm
	if s.peerClient != nil {
		s.peerClient.SetQUICManager(qm)
	}
	if s.turnMgr != nil {
		s.turnMgr.SetQUICManager(qm)
	}
	s.natMu.Unlock()
	if publicKnown {
		s.Config.Logger.Info("Direct QUIC listener started", "publicAddr", publicUDPAddr)
	} else {
		s.Config.Logger.Info("Local QUIC listener started without a public endpoint; TURN relays remain available")
	}
}

// detectPublicReachability decides whether this node can serve as a Sponsor:
// CGNAT ranges never can, and a node with peers must be confirmed from outside.
func (s *Server) detectPublicReachability(ctx context.Context, extIP string) bool {
	if ctx.Err() != nil {
		return false
	}
	if utils.IsPrivateOrCGNATIP(extIP) {
		s.Config.Logger.Info("Node is behind CGNAT/Private range. Auto-detected as NOT a Sponsor.", "ip", extIP)
		return false
	}

	if s.Config.BootstrapNode == "" {
		s.Config.Logger.Info("No Bootstrap Node configured. Assuming publicly reachable Sponsor since IP is public.", "ip", extIP)
		return true
	}

	s.Config.Logger.Info("Requesting reachability probe from Bootstrap Node...", "bootstrap", s.Config.BootstrapNode)
	probeCtx, cancel := context.WithTimeout(ctx, PeerRPCSTUN)
	defer cancel()

	probeResp, err := s.peerClient.RequestProbe(probeCtx, s.Config.BootstrapNode, protocol.ProbeRequest{
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
	if s.shuttingDown() || mappedUDP <= 0 || extIP == "" {
		return
	}
	s.natMu.Lock()
	defer s.natMu.Unlock()
	host := extIP
	if s.publicUDPAddr != "" {
		if h, _, err := net.SplitHostPort(s.publicUDPAddr); err == nil && h != "" {
			host = h
		}
	}
	newAddr := net.JoinHostPort(host, strconv.Itoa(mappedUDP))
	s.publicUDPAddr = newAddr
	if s.quicMgr != nil {
		s.quicMgr.SetPublicUDPAddr(newAddr)
	}
	s.Config.Logger.Info("Updated public UDP address from NAT mapping", "publicUDP", newAddr)
}

func (s *Server) IsSponsorNode() bool {
	return s.CurrentNATState().IsSponsor
}
