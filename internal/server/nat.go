package server

import (
	"context"
	"fmt"
	"net"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"time"
)

func (s *Server) determineSponsorAndNATStatus() {
	s.Config.Logger.Info("Determining NAT and Sponsor status...")

	// 1. Check override
	if s.Config.IsSponsorOverride != nil {
		s.isSponsor = *s.Config.IsSponsorOverride
		s.Config.Logger.Info("Sponsor status manually overridden", "isSponsor", s.isSponsor)
		return
	}

	// 2. Perform STUN check to get public IP
	stunServer := s.Config.STUNServer
	if stunServer == "" {
		stunServer = "stun.l.google.com:19302"
	}

	var extIP string
	var extPort int
	var conn *net.UDPConn
	var err error

	extIP, extPort, conn, err = utils.GetExternalUDPListener(stunServer, 5*time.Second)
	stunSuccess := err == nil

	if !stunSuccess {
		s.Config.Logger.Warn("STUN check failed, trying UPnP fallback to discover gateway", "error", err)
		if s.Config.DisableUPnP {
			s.isSponsor = false
			return
		}
		// Bind a local UDP socket to use for QUIC
		var listenErr error
		conn, listenErr = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if listenErr != nil {
			s.Config.Logger.Warn("Could not bind local UDP socket after STUN failure", "error", listenErr)
			s.isSponsor = false
			return
		}
	} else {
		s.Config.Logger.Debug("STUN public IP detected", "ip", extIP, "port", extPort)
	}

	// Try UPnP/NAT-PMP mapping if enabled (default)
	if !s.Config.DisableUPnP {
		tcpPortStr := utils.ExtractPort(s.Config.Address)
		tcpPort := 8443
		if tcpPortStr != "" {
			_, _ = fmt.Sscanf(tcpPortStr, "%d", &tcpPort)
		}
		udpPort := conn.LocalAddr().(*net.UDPAddr).Port

		s.natMapper = p2p.NewNATMapper(s.Config.Logger, tcpPort, udpPort)
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
		s.Config.Logger.Warn("Could not determine public IP (both STUN and UPnP failed), assuming private/CGNAT network")
		_ = conn.Close()
		s.isSponsor = false
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
			if httpClient, ok := s.peerClient.(*p2p.HTTPPeerClient); ok {
				httpClient.SetQUICManager(s.quicMgr)
			}
		}
	}

	// Check if the IP is private or CGNAT
	if utils.IsPrivateOrCGNATIP(extIP) {
		s.Config.Logger.Info("Node is behind CGNAT/Private range. Auto-detected as NOT a Sponsor.", "ip", extIP)
		s.isSponsor = false
		return
	}

	// 3. Probe ourselves via a peer in the cluster (Bootstrap Node)
	if s.Config.BootstrapNode != "" {
		// Parse our own listening port
		ownPort := utils.ExtractPort(s.Config.Address)
		if ownPort == "" {
			ownPort = "8443"
		}
		if s.natMapper != nil {
			mappedTCP, _ := s.natMapper.GetMappedPorts()
			if mappedTCP > 0 {
				ownPort = fmt.Sprintf("%d", mappedTCP)
			}
		}

		s.Config.Logger.Info("Requesting reachability probe from Bootstrap Node...", "bootstrap", s.Config.BootstrapNode)
		
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		probeReq := protocol.ProbeRequest{
			Address: fmt.Sprintf("https://%s:%s", extIP, ownPort),
		}
		
		probeResp, err := s.peerClient.RequestProbe(ctx, s.Config.BootstrapNode, probeReq)
		if err != nil {
			s.Config.Logger.Warn("Probe request to Bootstrap Node failed, assuming firewalled", "error", err)
			s.isSponsor = false
			return
		}

		if !probeResp.Reachable {
			s.Config.Logger.Info("Node port is unreachable from outside (Firewalled). Auto-detected as NOT a Sponsor.", "error", probeResp.Error)
			s.isSponsor = false
			return
		}

		s.Config.Logger.Info("Node is publicly reachable. Auto-detected as a Sponsor!")
		s.isSponsor = true
	} else {
		// If we are the Bootstrap Node (no bootstrap node configured) and we have a public IP, we assume we are a Sponsor.
		s.Config.Logger.Info("No Bootstrap Node configured. Assuming publicly reachable Sponsor since IP is public.", "ip", extIP)
		s.isSponsor = true
	}
}

func (s *Server) IsSponsorNode() bool {
	return s.isSponsor
}
