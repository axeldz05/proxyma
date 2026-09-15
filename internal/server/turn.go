package server

import (
	"fmt"
	"net"
	"strings"

	"github.com/pion/turn/v5"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// Relay port range served by the embedded TURN server for allocations.
const (
	turnRelayPortMin = 49160
	turnRelayPortMax = 49200
)

// turnServer is the embedded pion/turn listener that relays mesh traffic for
// peers that cannot reach each other directly. It serves one listener per
// address family with a known relay IP.
type turnServer struct {
	srv     *turn.Server
	configs []turn.PacketConnConfig
	conns   []net.PacketConn
	addrs   []string
}

// maybeStartTURNServer starts the embedded TURN server once per node lifetime
// when turn_enabled is set. publicIP is the best-known public address at NAT
// check time; relay IPs per family resolve as turn_public_addr > discovered
// public IP > local routable IP.
func (s *Server) maybeStartTURNServer(publicIP string) {
	if !s.Config.TURNEnabled || s.shuttingDown() {
		return
	}
	s.natMu.Lock()
	if s.turnSrv != nil {
		s.natMu.Unlock()
		return
	}
	s.natMu.Unlock()

	relayV4, relayV6 := s.turnRelayAddresses(publicIP)
	if relayV4 == "" && relayV6 == "" {
		s.Config.Logger.Warn("No public or local IP available for TURN relay addresses; " +
			"relayed endpoints will only be usable from this host (set turn_public_addr)")
		relayV4 = "0.0.0.0"
	}
	ts, err := startTURNServer(s.Config, relayV4, relayV6)
	if err != nil {
		s.Config.Logger.Error("Failed to start embedded TURN server", "error", err)
		return
	}
	s.natMu.Lock()
	if s.shuttingDown() || s.turnSrv != nil {
		s.natMu.Unlock()
		ts.Close()
		return
	}
	s.turnSrv = ts
	s.natMu.Unlock()
	s.Config.Logger.Info("Embedded TURN server started",
		"listen", ts.addrs, "relayIPv4", relayV4, "relayIPv6", relayV6,
		"relayPorts", fmt.Sprintf("%d-%d", turnRelayPortMin, turnRelayPortMax))
}

// turnRelayAddresses picks the IPv4 and IPv6 addresses advertised inside
// XOR-RELAYED-ADDRESS. turn_public_addr accepts a comma-separated list; an
// empty family means that family is not served.
func (s *Server) turnRelayAddresses(publicIP string) (string, string) {
	var configured []string
	if s.Config.TURNPublicAddr != "" {
		configured = strings.Split(s.Config.TURNPublicAddr, ",")
	}
	var localIPs []net.IP
	if ips, err := utils.GetRoutableLocalIPs(); err == nil {
		localIPs = ips
	}
	pick := func(wantV4 bool) string {
		for _, pool := range [][]string{configured, {publicIP}} {
			for _, raw := range pool {
				ip := net.ParseIP(strings.TrimSpace(raw))
				if ip == nil {
					continue
				}
				if (ip.To4() != nil) == wantV4 {
					return ip.String()
				}
			}
		}
		for _, ip := range localIPs {
			if (ip.To4() != nil) == wantV4 {
				return ip.String()
			}
		}
		return ""
	}
	return pick(true), pick(false)
}

// startTURNServer builds the pion TURN server with one UDP listener per family
// that has a relay IP. It refuses to run without a shared secret so the node
// can never become an open relay.
func startTURNServer(cfg protocol.NodeConfig, relayV4, relayV6 string) (*turnServer, error) {
	if cfg.TURNSecret == "" {
		return nil, fmt.Errorf("turn_enabled requires turn_secret: refusing to start an unauthenticated relay")
	}
	if relayV4 == "" && relayV6 == "" {
		return nil, fmt.Errorf("no relay IP for any address family")
	}
	listenAddr := cfg.TURNListenAddr
	if listenAddr == "" {
		listenAddr = protocol.TURNDefaultListenAddr
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid TURN listen address %q: %w", listenAddr, err)
	}

	ts := &turnServer{}
	if relayV4 != "" {
		if err := ts.appendFamily(cfg, "udp4", listenAddr, relayV4); err != nil {
			ts.closeConns()
			return nil, err
		}
	}
	if relayV6 != "" {
		// IPv6 is best effort: skip it when the host cannot bind, but only
		// while the IPv4 listener already exists.
		if err := ts.appendFamily(cfg, "udp6", net.JoinHostPort("::", port), relayV6); err != nil {
			if len(ts.conns) == 0 {
				ts.closeConns()
				return nil, err
			}
			if cfg.Logger != nil {
				cfg.Logger.Warn("IPv6 TURN listener unavailable; serving IPv4 only", "error", err)
			}
		}
	}
	if len(ts.configs) == 0 {
		ts.closeConns()
		return nil, fmt.Errorf("no TURN listener could be started")
	}

	srv, err := turn.NewServer(turn.ServerConfig{
		PacketConnConfigs: ts.configs,
		Realm:             realm(cfg),
		AuthHandler:       turn.LongTermTURNRESTAuthHandler(cfg.TURNSecret, nil),
		LoggerFactory:     utils.PionLoggerFactory(cfg.Logger),
	})
	if err != nil {
		ts.closeConns()
		return nil, fmt.Errorf("create TURN server: %w", err)
	}
	ts.srv = srv
	return ts, nil
}

func realm(cfg protocol.NodeConfig) string {
	if cfg.TURNRealm == "" {
		return protocol.TURNDefaultRealm
	}
	return cfg.TURNRealm
}

func (t *turnServer) appendFamily(cfg protocol.NodeConfig, network, bindAddr, relayIP string) error {
	relayAddress := net.ParseIP(relayIP)
	if relayAddress == nil {
		return fmt.Errorf("invalid TURN relay IP %q", relayIP)
	}
	conn, err := net.ListenPacket(network, bindAddr)
	if err != nil {
		return fmt.Errorf("listen TURN %s %s: %w", network, bindAddr, err)
	}
	wildcard := "0.0.0.0"
	if network == "udp6" {
		wildcard = "::"
	}
	t.conns = append(t.conns, conn)
	t.addrs = append(t.addrs, conn.LocalAddr().String())
	t.configs = append(t.configs, turn.PacketConnConfig{
		PacketConn: conn,
		RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
			RelayAddress: relayAddress,
			Address:      wildcard,
			MinPort:      turnRelayPortMin,
			MaxPort:      turnRelayPortMax,
			MaxRetries:   100,
		},
	})
	return nil
}

func (t *turnServer) closeConns() {
	for _, conn := range t.conns {
		_ = conn.Close()
	}
	t.conns = nil
	t.configs = nil
}

func (t *turnServer) Close() {
	if t.srv != nil {
		_ = t.srv.Close()
	}
	t.closeConns()
}
