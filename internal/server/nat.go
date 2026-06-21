package server

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
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

	extIP, _, err := utils.GetExternalIPPort(stunServer, 5*time.Second)
	if err != nil {
		s.Config.Logger.Warn("STUN check failed, assuming private/CGNAT network", "error", err)
		s.isSponsor = false
		return
	}

	s.Config.Logger.Debug("STUN public IP detected", "ip", extIP)

	// Check if the IP is private or CGNAT
	if utils.IsPrivateOrCGNATIP(extIP) {
		s.Config.Logger.Info("Node is behind CGNAT/Private range. Auto-detected as NOT a Sponsor.", "ip", extIP)
		s.isSponsor = false
		return
	}

	// 3. Probe ourselves via a peer in the cluster (Bootstrap Node)
	if s.Config.BootstrapNode != "" {
		// Parse our own listening port
		parsedOwn, err := url.Parse(s.Config.Address)
		var ownPort string
		if err == nil {
			_, p, err := net.SplitHostPort(parsedOwn.Host)
			if err == nil {
				ownPort = p
			}
		}
		if ownPort == "" {
			// fallback/default port
			if strings.Contains(s.Config.Address, ":") {
				parts := strings.Split(s.Config.Address, ":")
				ownPort = parts[len(parts)-1]
			} else {
				ownPort = "8443"
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
