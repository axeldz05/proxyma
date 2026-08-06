package server

import (
	"context"
	"fmt"
	"net"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
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
	if s.natMapper != nil {
		if mappedTCP, _ := s.natMapper.GetMappedPorts(); mappedTCP > 0 {
			return fmt.Sprintf("%d", mappedTCP)
		}
	}
	return portStr
}

func (s *Server) determineSponsorAndNATStatus() {
	s.Config.Logger.Info("Determining NAT and Sponsor status...")

	sponsorOverride := s.Config.IsSponsorOverride

	// Perform STUN check to get public IP (override only sets isSponsor; do not skip QUIC/NAT).
	stunServer := s.Config.STUNServer
	if stunServer == "" {
		stunServer = "stun.l.google.com:19302"
	}

	var extIP string
	var extPort int
	var conn *net.UDPConn
	var err error

	extIP, extPort, conn, err = utils.GetExternalUDPListener(stunServer, PeerRPCSTUN)
	stunSuccess := err == nil

	if !stunSuccess {
		s.Config.Logger.Warn("STUN check failed, trying UPnP fallback to discover gateway", "error", err)
		if s.Config.DisableUPnP {
			if sponsorOverride != nil {
				s.isSponsor = *sponsorOverride
			} else {
				s.isSponsor = false
			}
			return
		}
		// Bind a local UDP socket to use for QUIC
		var listenErr error
		conn, listenErr = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if listenErr != nil {
			s.Config.Logger.Warn("Could not bind local UDP socket after STUN failure", "error", listenErr)
			if sponsorOverride != nil {
				s.isSponsor = *sponsorOverride
			} else {
				s.isSponsor = false
			}
			return
		}
	} else {
		s.Config.Logger.Debug("STUN public IP detected", "ip", extIP, "port", extPort)
	}

	// Try UPnP/NAT-PMP mapping if enabled (default)
	if !s.Config.DisableUPnP {
		_, tcpPort := s.configTCPPort()
		udpPort := conn.LocalAddr().(*net.UDPAddr).Port

		s.natMapper = p2p.NewNATMapper(s.Config.Logger, tcpPort, udpPort)
		s.natMapper.SetOnMapped(func(mappedTCP, mappedUDP int) {
			s.refreshPublicUDPFromMapping(extIP, mappedUDP)
		})
		s.natMapper.Start()

		mappedTCP, mappedUDP := s.natMapper.GetMappedPorts()
		if mappedTCP > 0 {
			tcpPort = mappedTCP
		}
		if mappedUDP > 0 {
			extPort = mappedUDP
		}

		// If STUN failed but UPnP succeeded, query router for our external IP
		if !stunSuccess && mappedTCP > 0 {
			routerIP, routerErr := s.natMapper.GetExternalAddress()
			if routerErr == nil && routerIP != nil {
				extIP = routerIP.String()
				stunSuccess = true // We have a valid public IP now!
				s.Config.Logger.Info("UPnP successfully mapped ports and retrieved public IP from gateway", "ip", extIP, "tcpPort", tcpPort, "udpPort", extPort)
			}
		}
	}

	// If we still don't have a valid STUN/UPnP external IP, we cannot act as a Sponsor
	if !stunSuccess {
		s.Config.Logger.Warn("Could not determine public IP, assuming private/CGNAT network")
		_ = conn.Close()
		if sponsorOverride != nil {
			s.isSponsor = *sponsorOverride
			s.Config.Logger.Info("Sponsor status manually overridden", "isSponsor", s.isSponsor)
		} else {
			s.isSponsor = false
		}
		return
	}

	// Initialize QUIC Manager with the socket used for STUN query
	s.tlsMutex.RLock()
	stls := s.serverTLSConfig
	ctls := s.clientTLSConfig
	s.tlsMutex.RUnlock()

	if stls == nil || ctls == nil {
		_ = conn.Close()
	} else {
		s.udpConn = conn
		s.publicUDPAddr = fmt.Sprintf("%s:%d", extIP, extPort)
		s.quicMgr = p2p.NewQUICManager(s.Config.ID, conn, ctls, stls, s.handler, s.Config.Logger)
		s.quicMgr.PublicUDPAddr = s.publicUDPAddr

		if err := s.quicMgr.StartListener(); err != nil {
			s.Config.Logger.Error("Failed to start QUIC listener", "error", err)
		} else {
			s.Config.Logger.Info("Direct QUIC listener started", "publicAddr", s.publicUDPAddr)
			s.peerClient.SetQUICManager(s.quicMgr)
		}
	}

	// Check if the IP is private or CGNAT
	if utils.IsPrivateOrCGNATIP(extIP) {
		s.Config.Logger.Info("Node is behind CGNAT/Private range. Auto-detected as NOT a Sponsor.", "ip", extIP)
		s.isSponsor = false
	} else if s.Config.BootstrapNode != "" {
		// Probe ourselves via a peer in the cluster (Bootstrap Node)
		ownPort := s.advertisedTCPPort()

		s.Config.Logger.Info("Requesting reachability probe from Bootstrap Node...", "bootstrap", s.Config.BootstrapNode)

		ctx, cancel := context.WithTimeout(context.Background(), PeerRPCSTUN)
		defer cancel()

		probeReq := protocol.ProbeRequest{
			Address: fmt.Sprintf("https://%s:%s", extIP, ownPort),
		}

		probeResp, err := s.peerClient.RequestProbe(ctx, s.Config.BootstrapNode, probeReq)
		if err != nil {
			s.Config.Logger.Warn("Probe request to Bootstrap Node failed, assuming firewalled", "error", err)
			s.isSponsor = false
		} else if !probeResp.Reachable {
			s.Config.Logger.Info("Node port is unreachable from outside (Firewalled). Auto-detected as NOT a Sponsor.", "error", probeResp.Error)
			s.isSponsor = false
		} else {
			s.Config.Logger.Info("Node is publicly reachable. Auto-detected as a Sponsor!")
			s.isSponsor = true
		}
	} else {
		// If we are the Bootstrap Node (no bootstrap node configured) and we have a public IP, we assume we are a Sponsor.
		s.Config.Logger.Info("No Bootstrap Node configured. Assuming publicly reachable Sponsor since IP is public.", "ip", extIP)
		s.isSponsor = true
	}

	if sponsorOverride != nil {
		s.isSponsor = *sponsorOverride
		s.Config.Logger.Info("Sponsor status manually overridden", "isSponsor", s.isSponsor)
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
