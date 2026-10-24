package p2p

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/turn/v5"
	"github.com/quic-go/quic-go"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// TURNManagerConfig configures a TURNManager. ServerAddr is a TURN server
// "host:port", Secret is the cluster-wide TURN shared secret used to derive
// REST-style ephemeral credentials (username "<expiry>:<peerID>").
type TURNManagerConfig struct {
	ServerAddr string
	Secret     string
	Realm      string
	LocalID    string
	Logger     Logger
}

// TURNManager owns this node's TURN client allocation. It lazily allocates a
// relayed transport address on the configured TURN server, publishes it to the
// QUICManager as a secondary transport, and dials peers through it when hole
// punching fails. The zero usable state is "no allocation"; Close releases it.
type TURNManager struct {
	cfg TURNManagerConfig
	qm  *QUICManager

	mu          sync.Mutex
	conn        *net.UDPConn
	client      *turn.Client
	relayed     net.PacketConn
	relayedAddr string
	credExpiry  time.Time
	permitted   map[string]net.IP
	desiredIPs  map[string]net.IP
	// Resolution of the TURN server per family; the allocation family is
	// chosen per allocateFamilyLocked and pinned in activeNetwork.
	serverV4      *net.UDPAddr
	serverV6      *net.UDPAddr
	activeNetwork string
	serverAddrIP  net.IP
	closed        bool
}

func NewTURNManager(cfg TURNManagerConfig) (*TURNManager, error) {
	if cfg.ServerAddr == "" {
		return nil, fmt.Errorf("TURN manager requires a turn_server address")
	}
	if cfg.Secret == "" {
		return nil, fmt.Errorf("TURN manager requires a turn_secret")
	}
	if cfg.Realm == "" {
		cfg.Realm = protocol.TURNDefaultRealm
	}
	var serverV4, serverV6 *net.UDPAddr
	if addr, err := net.ResolveUDPAddr("udp4", cfg.ServerAddr); err == nil {
		serverV4 = addr
	}
	if addr, err := net.ResolveUDPAddr("udp6", cfg.ServerAddr); err == nil {
		serverV6 = addr
	}
	return &TURNManager{
		cfg:        cfg,
		permitted:  make(map[string]net.IP),
		desiredIPs: make(map[string]net.IP),
		serverV4:   serverV4,
		serverV6:   serverV6,
	}, nil
}

// SetQUICManager attaches the manager whose sessions map should carry QUIC
// sessions established over the TURN allocation.
func (tm *TURNManager) SetQUICManager(qm *QUICManager) {
	tm.mu.Lock()
	tm.qm = qm
	tm.mu.Unlock()
}

// RelayedAddress returns the allocated relayed "ip:port", or "" when no
// allocation is active. Safe to call from the presence announcer.
func (tm *TURNManager) RelayedAddress() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.relayedAddr
}

// EnsureAllocation returns the relayed address, allocating on first use (or
// re-allocating once the credential window is nearly spent). The allocation is
// refreshed automatically by the pion client while the credential stays valid.
func (tm *TURNManager) EnsureAllocation(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed {
		return "", fmt.Errorf("TURN manager is closed")
	}
	if tm.relayed != nil && time.Now().Before(tm.credExpiry) {
		return tm.relayedAddr, nil
	}
	if tm.relayed != nil {
		tm.teardownLocked()
	}
	return tm.allocateLocked(ctx)
}

func (tm *TURNManager) allocateLocked(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	network, serverAddr, family, err := tm.allocateFamilyLocked()
	if err != nil {
		return "", err
	}
	username, password, err := turn.GenerateLongTermTURNRESTCredentials(
		tm.cfg.Secret, tm.cfg.LocalID, protocol.TURNCredentialTTL)
	if err != nil {
		return "", fmt.Errorf("generate TURN credentials: %w", err)
	}

	bindIP := net.IPv4zero
	if network == "udp6" {
		bindIP = net.IPv6unspecified
	}
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: bindIP, Port: 0})
	if err != nil {
		return "", fmt.Errorf("bind TURN client socket: %w", err)
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		TURNServerAddr:         serverAddr.String(),
		Conn:                   conn,
		Username:               username,
		Password:               password,
		Realm:                  tm.cfg.Realm,
		RequestedAddressFamily: family,
		LoggerFactory:          utils.PionLoggerFactory(tm.cfg.Logger),
	})
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("create TURN client: %w", err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		_ = conn.Close()
		return "", fmt.Errorf("start TURN client: %w", err)
	}
	relayed, err := client.Allocate()
	if err != nil {
		client.Close()
		_ = conn.Close()
		return "", fmt.Errorf("TURN allocation on %s: %w", tm.cfg.ServerAddr, err)
	}

	tm.conn = conn
	tm.client = client
	tm.relayed = relayed
	tm.relayedAddr = relayed.LocalAddr().String()
	tm.credExpiry = time.Now().Add(protocol.TURNCredentialTTL)
	tm.activeNetwork = network
	tm.serverAddrIP = serverAddr.IP

	if tm.qm != nil {
		if err := tm.qm.AttachTURNConn(relayed); err != nil {
			tm.teardownLocked()
			return "", fmt.Errorf("attach TURN transport to QUIC manager: %w", err)
		}
	}

	// Permissions for the peer IPs learned so far plus the TURN server itself
	// (covers relay-to-relay flows when both peers allocate on the same server).
	tm.permitted = make(map[string]net.IP)
	tm.applyDesiredPermissionsLocked()

	if tm.cfg.Logger != nil {
		tm.cfg.Logger.Info("TURN allocation established",
			"server", tm.cfg.ServerAddr, "relayed", tm.relayedAddr)
	}
	return tm.relayedAddr, nil
}

// DialPeer ensures an allocation, creates permissions for the peer's public
// addresses and dials a QUIC session to the first reachable candidate through
// the TURN transport.
func (tm *TURNManager) DialPeer(ctx context.Context, peerID string, remoteAddresses []string) (*quic.Conn, error) {
	if _, err := tm.EnsureAllocation(ctx); err != nil {
		return nil, err
	}
	tm.mu.Lock()
	relayed := tm.relayed
	qm := tm.qm
	tm.mu.Unlock()
	if relayed == nil {
		return nil, fmt.Errorf("TURN allocation is not active")
	}
	if qm == nil {
		return nil, fmt.Errorf("QUIC manager is not attached to the TURN manager")
	}

	candidates, err := tm.resolveCandidates(relayed, remoteAddresses)
	if err != nil {
		return nil, err
	}
	tm.mu.Lock()
	permErr := tm.createPermissionsLocked(ipAddrsOf(candidates))
	tm.mu.Unlock()
	if permErr != nil {
		return nil, fmt.Errorf("create TURN permissions: %w", permErr)
	}

	var attemptErrors []error
	for _, candidate := range candidates {
		conn, dialErr := qm.DialOverTURN(ctx, peerID, candidate)
		if dialErr != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", candidate, dialErr))
			continue
		}
		if tm.cfg.Logger != nil {
			tm.cfg.Logger.Info("QUIC session established over TURN", "peer", peerID, "relayed", tm.RelayedAddress())
		}
		return conn, nil
	}
	return nil, fmt.Errorf("TURN dial to %s failed: %w", peerID, errors.Join(attemptErrors...))
}

// SetPeerAddresses records the public addresses a peer announced so inbound
// flows from them are permitted on our allocation. Only IP literals are used;
// permissions for hostnames materialize when the node STUN address is known.
func (tm *TURNManager) SetPeerAddresses(addrs []string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed {
		return
	}
	for _, ip := range IPLiteralsFromAddresses(addrs) {
		tm.desiredIPs[ip.String()] = ip
	}
	if tm.client != nil {
		tm.applyDesiredPermissionsLocked()
	}
}

// ForgetPeerAddresses drops a removed peer's addresses from the desired
// permission set. Existing permissions expire server-side after the timeout.
func (tm *TURNManager) ForgetPeerAddresses(addrs []string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, ip := range IPLiteralsFromAddresses(addrs) {
		delete(tm.desiredIPs, ip.String())
	}
}

// allocateFamilyLocked chooses the address family for the next allocation.
// IPv6 wins only when the TURN server is reachable over IPv6 and every
// desired peer IP is IPv6; otherwise IPv4 stays the default so existing
// deployments keep their behavior. The chosen family must be one the server
// actually resolves to.
func (tm *TURNManager) allocateFamilyLocked() (network string, serverAddr *net.UDPAddr, family turn.RequestedAddressFamily, err error) {
	wantV6 := len(tm.desiredIPs) > 0
	for _, ip := range tm.desiredIPs {
		if ip.To4() != nil {
			wantV6 = false
			break
		}
	}
	if wantV6 && tm.serverV6 != nil {
		return "udp6", tm.serverV6, turn.RequestedAddressFamilyIPv6, nil
	}
	if tm.serverV4 != nil {
		return "udp4", tm.serverV4, turn.RequestedAddressFamilyIPv4, nil
	}
	if tm.serverV6 != nil {
		return "udp6", tm.serverV6, turn.RequestedAddressFamilyIPv6, nil
	}
	return "", nil, 0, fmt.Errorf("TURN server %q does not resolve to any address", tm.cfg.ServerAddr)
}

// applyDesiredPermissionsLocked creates permissions for desired IPs that do
// not have an active one. Callers must hold tm.mu.
func (tm *TURNManager) applyDesiredPermissionsLocked() {
	if tm.client == nil {
		return
	}
	ips := make([]net.IP, 0, len(tm.desiredIPs)+1)
	for key, ip := range tm.desiredIPs {
		if _, done := tm.permitted[key]; done {
			continue
		}
		if !sameFamily(ip, tm.activeNetwork) {
			continue // the relay socket cannot reach the other family
		}
		ips = append(ips, ip)
	}
	if tm.serverAddrIP != nil {
		if _, done := tm.permitted[tm.serverAddrIP.String()]; !done {
			ips = append(ips, tm.serverAddrIP)
		}
	}
	if len(ips) == 0 {
		return
	}
	if err := tm.createPermissionsLocked(ips); err != nil && tm.cfg.Logger != nil {
		tm.cfg.Logger.Warn("TURN permission setup failed", "error", err)
	}
}

func sameFamily(ip net.IP, network string) bool {
	return utils.SameIPFamily(ip, network)
}

// createPermissions sends CreatePermission for every IP and records them as
// active. Callers must hold tm.mu.
func (tm *TURNManager) createPermissionsLocked(ips []net.IP) error {
	if tm.client == nil {
		return fmt.Errorf("no active TURN allocation")
	}
	if err := tm.client.CreatePermission(udpAddrsOf(ips)...); err != nil {
		return err
	}
	for _, ip := range ips {
		tm.permitted[ip.String()] = ip
	}
	return nil
}

// resolveCandidates parses quic:// addresses and keeps the ones whose family
// matches the TURN allocation conn, deduplicated.
func (tm *TURNManager) resolveCandidates(relayed net.PacketConn, remoteAddresses []string) ([]*net.UDPAddr, error) {
	var (
		candidates []*net.UDPAddr
		failures   []error
		seen       = make(map[string]struct{})
	)
	for _, raw := range remoteAddresses {
		hostport, ok := ParseQUICAddr(raw)
		if !ok {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", hostport)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", raw, err))
			continue
		}
		if err := requireMatchingUDPFamily(relayed, addr); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", raw, err))
			continue
		}
		key := addr.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, addr)
	}
	if len(candidates) == 0 {
		if len(failures) == 0 {
			return nil, fmt.Errorf("no quic:// addresses")
		}
		return nil, errors.Join(failures...)
	}
	return candidates, nil
}

// Close releases the allocation and the TURN client. Active sessions riding
// the allocation die with it; the router falls back to the HTTP relay.
func (tm *TURNManager) Close() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.closed = true
	tm.teardownLocked()
}

func (tm *TURNManager) teardownLocked() {
	if tm.relayed != nil {
		_ = tm.relayed.Close()
		tm.relayed = nil
	}
	tm.relayedAddr = ""
	tm.credExpiry = time.Time{}
	tm.activeNetwork = ""
	tm.serverAddrIP = nil
	if tm.client != nil {
		tm.client.Close()
		tm.client = nil
	}
	if tm.conn != nil {
		_ = tm.conn.Close()
		tm.conn = nil
	}
}

// IPLiteralsFromAddresses extracts unique IPv4/IPv6 literals from address
// records (quic://, https or bare host:port). Hostnames are skipped.
func IPLiteralsFromAddresses(addrs []string) []net.IP {
	var (
		ips  []net.IP
		seen = make(map[string]struct{})
	)
	for _, raw := range addrs {
		hostport, ok := ParseQUICAddr(raw)
		if !ok {
			hostport = raw
		}
		host, _, err := net.SplitHostPort(hostport)
		if err != nil {
			host = hostport
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		key := ip.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		ips = append(ips, ip)
	}
	return ips
}

func ipAddrsOf(addrs []*net.UDPAddr) []net.IP {
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP != nil {
			ips = append(ips, addr.IP)
		}
	}
	return ips
}

func udpAddrsOf(ips []net.IP) []net.Addr {
	addrs := make([]net.Addr, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, &net.UDPAddr{IP: ip, Port: 0})
	}
	return addrs
}
