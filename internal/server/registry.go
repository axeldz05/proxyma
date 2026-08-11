package server

import (
	"crypto/x509"
	"log/slog"
	"maps"
	"sync"

	"proxyma/internal/protocol"
)

// peerState is everything the registry knows about one peer. One entry per peer under a
// single lock replaces five parallel maps, so a peer can never be observed half-updated
// and a new per-peer attribute is one field instead of a map plus a mutex plus a purge.
type peerState struct {
	record protocol.AddressRecord
	// hasRecord separates a registered peer from an entry created by a certificate,
	// a service push or an offline mark. Presence in the map is NOT proof of
	// registration: mTLSGuard and the relay ask GetPeerRecord for that, and a peer
	// that only ever presented a certificate must not pass as registered.
	hasRecord bool
	online    bool
	lastError string
	services  map[string]protocol.ServiceSchema
	cert      *x509.Certificate
}

// PeerRegistry manages all cluster peers, their status, and their registered service schemas.
type PeerRegistry struct {
	logger *slog.Logger
	nodeID string
	mu     sync.RWMutex
	peers  map[string]*peerState
}

// NewPeerRegistry creates and initializes a new PeerRegistry.
func NewPeerRegistry(logger *slog.Logger, nodeID string) *PeerRegistry {
	return &PeerRegistry{
		logger: logger,
		nodeID: nodeID,
		peers:  make(map[string]*peerState),
	}
}

// stateLocked returns the entry for peerID, creating an empty one when absent.
// Caller must hold mu for writing.
func (pr *PeerRegistry) stateLocked(peerID string) *peerState {
	st, ok := pr.peers[peerID]
	if !ok {
		st = &peerState{}
		pr.peers[peerID] = st
	}
	return st
}

// AddPeer adds or updates a peer's address record. It returns true if the peer record was actually updated.
func (pr *PeerRegistry) AddPeer(peerID string, addressRecord protocol.AddressRecord) bool {
	if peerID == pr.nodeID {
		return false
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()

	// Clean up stale peers with the same primary address
	if len(addressRecord.Addresses) > 0 {
		newPrimaryAddr := addressRecord.Addresses[0]
		var staleIDs []string
		for id, st := range pr.peers {
			if id != peerID && st.hasRecord && len(st.record.Addresses) > 0 && st.record.Addresses[0] == newPrimaryAddr {
				staleIDs = append(staleIDs, id)
			}
		}

		for _, staleID := range staleIDs {
			delete(pr.peers, staleID)
			pr.logger.Info("Removing stale peer replaced by new peer ID at same address", "stalePeerID", staleID, "newPeerID", peerID, "address", newPrimaryAddr)
		}
	}

	st := pr.stateLocked(peerID)
	if st.hasRecord {
		existing := st.record
		if addressRecord.Sequence < existing.Sequence {
			pr.logger.Debug("Ignoring older peer address record", "peerID", peerID, "currentSeq", existing.Sequence, "newSeq", addressRecord.Sequence)
			return false
		}
		if addressRecord.Sequence == existing.Sequence {
			addrSet := make(map[string]bool)
			for _, a := range existing.Addresses {
				addrSet[a] = true
			}
			for _, a := range addressRecord.Addresses {
				addrSet[a] = true
			}
			var newAddrs []string
			for a := range addrSet {
				newAddrs = append(newAddrs, a)
			}
			addressRecord.Addresses = newAddrs
		}
	}

	st.record = addressRecord
	st.hasRecord = true
	markOnlineLocked(st)

	pr.logger.Info("peerID added to peers", "peerID", peerID, "node", pr.nodeID)
	return true
}

// RemovePeer removes a peer and its status, services and certificate from the registry.
func (pr *PeerRegistry) RemovePeer(peerID string) {
	pr.mu.Lock()
	delete(pr.peers, peerID)
	pr.mu.Unlock()

	pr.logger.Info("peerID removed from peers", "peerID", peerID)
}

// markOnlineLocked marks the peer online and clears any stored error.
func markOnlineLocked(st *peerState) {
	st.online = true
	st.lastError = ""
}

// SetPeerOnline marks a peer as online or offline.
func (pr *PeerRegistry) SetPeerOnline(peerID string, online bool) {
	if !online {
		pr.SetPeerOffline(peerID, nil)
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	markOnlineLocked(pr.stateLocked(peerID))
}

// SetPeerOffline marks a peer as offline and stores the connection error.
func (pr *PeerRegistry) SetPeerOffline(peerID string, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	st := pr.stateLocked(peerID)
	st.online = false
	if err != nil {
		st.lastError = "offline or could not reach: " + err.Error()
	} else {
		st.lastError = "offline"
	}
}

// GetPeerError retrieves the connection error for a specific peer.
func (pr *PeerRegistry) GetPeerError(peerID string) string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	if st, ok := pr.peers[peerID]; ok {
		return st.lastError
	}
	return ""
}

// IsPeerOnline checks if a peer is online.
func (pr *PeerRegistry) IsPeerOnline(peerID string) bool {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	if st, ok := pr.peers[peerID]; ok {
		return st.online
	}
	return false
}

// primaryPeerAddrs projects registered peers to their primary address (caller must hold mu).
func primaryPeerAddrs(peers map[string]*peerState, filter func(protocol.AddressRecord) bool) map[string]string {
	out := make(map[string]string)
	for id, st := range peers {
		if !st.hasRecord {
			continue
		}
		if filter != nil && !filter(st.record) {
			continue
		}
		if len(st.record.Addresses) > 0 {
			out[id] = st.record.Addresses[0]
		}
	}
	return out
}

// GetPeersCopy returns a mapping of all peer IDs to their primary address.
func (pr *PeerRegistry) GetPeersCopy() map[string]string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return primaryPeerAddrs(pr.peers, nil)
}

// GetPeerRecord retrieves the address record of a specific peer. ok is false for a peer
// that never announced an address, even if the registry holds other state for it.
func (pr *PeerRegistry) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	st, ok := pr.peers[peerID]
	if !ok || !st.hasRecord {
		return protocol.AddressRecord{}, false
	}
	return st.record, true
}

// GetPeersRecordCopy returns a copy of all peer address records.
func (pr *PeerRegistry) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	snapshot := make(map[string]protocol.AddressRecord, len(pr.peers))
	for id, st := range pr.peers {
		if st.hasRecord {
			snapshot[id] = st.record
		}
	}
	return snapshot
}

// GetSponsorPeers returns a mapping of all peer IDs to their primary address if they are Sponsors.
func (pr *PeerRegistry) GetSponsorPeers() map[string]string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return primaryPeerAddrs(pr.peers, func(v protocol.AddressRecord) bool { return v.IsSponsor })
}

// GetClusterServices returns the registered services of a specific peer.
func (pr *PeerRegistry) GetClusterServices(peerID string) map[string]protocol.ServiceSchema {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	services := make(map[string]protocol.ServiceSchema)
	if st, ok := pr.peers[peerID]; ok {
		maps.Copy(services, st.services)
	}
	return services
}

// GetServiceSchema searches all peers for the given service schema.
func (pr *PeerRegistry) GetServiceSchema(serviceName string) (protocol.ServiceSchema, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	for _, st := range pr.peers {
		if schema, ok := st.services[serviceName]; ok {
			return schema, true
		}
	}
	return protocol.ServiceSchema{}, false
}

// UpdatePeerService updates a peer service schema.
func (pr *PeerRegistry) UpdatePeerService(peerID string, action string, schema protocol.ServiceSchema) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	st := pr.stateLocked(peerID)
	if st.services == nil {
		st.services = make(map[string]protocol.ServiceSchema)
	}

	switch action {
	case protocol.ActionAdd, protocol.ActionModify:
		st.services[schema.Name] = schema
		pr.logger.Info("Cluster service registered", "service", schema.Name, "peer", peerID)
	case protocol.ActionRemove:
		delete(st.services, schema.Name)
		pr.logger.Info("Cluster service removed", "service", schema.Name, "peer", peerID)
	}
}

func (pr *PeerRegistry) SetPeerCertificate(peerID string, cert *x509.Certificate) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.stateLocked(peerID).cert = cert
}

func (pr *PeerRegistry) GetPeerCertificate(peerID string) (*x509.Certificate, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	st, ok := pr.peers[peerID]
	if !ok || st.cert == nil {
		return nil, false
	}
	return st.cert, true
}
