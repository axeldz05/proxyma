package server

import (
	"crypto/x509"
	"log/slog"
	"maps"
	"sync"

	"proxyma/internal/protocol"
)

// PeerRegistry manages all cluster peers, their status, and their registered service schemas.
type PeerRegistry struct {
	logger            *slog.Logger
	nodeID            string
	peers             map[string]protocol.AddressRecord
	peersMu           sync.RWMutex
	activePeers       map[string]bool
	activePeersMu     sync.RWMutex
	clusterServices   map[string]map[string]protocol.ServiceSchema
	clusterServicesMu sync.RWMutex
	peerCerts         map[string]*x509.Certificate
	peerCertsMu       sync.RWMutex
}

// NewPeerRegistry creates and initializes a new PeerRegistry.
func NewPeerRegistry(logger *slog.Logger, nodeID string) *PeerRegistry {
	return &PeerRegistry{
		logger:          logger,
		nodeID:          nodeID,
		peers:           make(map[string]protocol.AddressRecord),
		activePeers:     make(map[string]bool),
		clusterServices: make(map[string]map[string]protocol.ServiceSchema),
		peerCerts:       make(map[string]*x509.Certificate),
	}
}

// AddPeer adds or updates a peer's address record. It returns true if the peer record was actually updated.
func (pr *PeerRegistry) AddPeer(peerID string, addressRecord protocol.AddressRecord) bool {
	pr.peersMu.Lock()
	defer pr.peersMu.Unlock()

	existing, exists := pr.peers[peerID]
	if exists {
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

	pr.peers[peerID] = addressRecord
	pr.SetPeerOnline(peerID, true)
	pr.logger.Info("peerID added to peers", "peerID", peerID, "node", pr.nodeID)
	return true
}

// RemovePeer removes a peer and its status/services from the registry.
func (pr *PeerRegistry) RemovePeer(peerID string) {
	pr.peersMu.Lock()
	delete(pr.peers, peerID)
	pr.peersMu.Unlock()

	pr.activePeersMu.Lock()
	delete(pr.activePeers, peerID)
	pr.activePeersMu.Unlock()

	pr.clusterServicesMu.Lock()
	delete(pr.clusterServices, peerID)
	pr.clusterServicesMu.Unlock()

	pr.logger.Info("peerID removed from peers", "peerID", peerID)
}

// SetPeerOnline marks a peer as online or offline.
func (pr *PeerRegistry) SetPeerOnline(peerID string, online bool) {
	pr.activePeersMu.Lock()
	defer pr.activePeersMu.Unlock()
	pr.activePeers[peerID] = online
}

// IsPeerOnline checks if a peer is online.
func (pr *PeerRegistry) IsPeerOnline(peerID string) bool {
	pr.activePeersMu.RLock()
	defer pr.activePeersMu.RUnlock()
	return pr.activePeers[peerID]
}

// GetPeersCopy returns a mapping of all peer IDs to their primary address.
func (pr *PeerRegistry) GetPeersCopy() map[string]string {
	pr.peersMu.RLock()
	defer pr.peersMu.RUnlock()
	peers := make(map[string]string, len(pr.peers))
	for k, v := range pr.peers {
		if len(v.Addresses) > 0 {
			peers[k] = v.Addresses[0]
		}
	}
	return peers
}

// GetPeerRecord retrieves the address record of a specific peer.
func (pr *PeerRegistry) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	pr.peersMu.RLock()
	defer pr.peersMu.RUnlock()
	record, exists := pr.peers[peerID]
	return record, exists
}

// GetPeersRecordCopy returns a copy of all peer address records.
func (pr *PeerRegistry) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	pr.peersMu.RLock()
	defer pr.peersMu.RUnlock()
	snapshot := make(map[string]protocol.AddressRecord, len(pr.peers))
	maps.Copy(snapshot, pr.peers)
	return snapshot
}

// GetSponsorPeers returns a mapping of all peer IDs to their primary address if they are Sponsors.
func (pr *PeerRegistry) GetSponsorPeers() map[string]string {
	pr.peersMu.RLock()
	defer pr.peersMu.RUnlock()
	sponsors := make(map[string]string)
	for k, v := range pr.peers {
		if v.IsSponsor && len(v.Addresses) > 0 {
			sponsors[k] = v.Addresses[0]
		}
	}
	return sponsors
}

// GetClusterServices returns the registered services of a specific peer.
func (pr *PeerRegistry) GetClusterServices(peerID string) map[string]protocol.ServiceSchema {
	pr.clusterServicesMu.RLock()
	defer pr.clusterServicesMu.RUnlock()
	services := make(map[string]protocol.ServiceSchema)
	if peerServices, ok := pr.clusterServices[peerID]; ok {
		maps.Copy(services, peerServices)
	}
	return services
}

// UpdatePeerService updates a peer service schema.
func (pr *PeerRegistry) UpdatePeerService(peerID string, action string, schema protocol.ServiceSchema) {
	pr.clusterServicesMu.Lock()
	defer pr.clusterServicesMu.Unlock()

	if pr.clusterServices[peerID] == nil {
		pr.clusterServices[peerID] = make(map[string]protocol.ServiceSchema)
	}

	switch action {
	case "add", "modify":
		pr.clusterServices[peerID][schema.Name] = schema
		pr.logger.Info("Cluster service registered", "service", schema.Name, "peer", peerID)
	case "remove":
		delete(pr.clusterServices[peerID], schema.Name)
		pr.logger.Info("Cluster service removed", "service", schema.Name, "peer", peerID)
	}
}

func (pr *PeerRegistry) SetPeerCertificate(peerID string, cert *x509.Certificate) {
	pr.peerCertsMu.Lock()
	defer pr.peerCertsMu.Unlock()
	pr.peerCerts[peerID] = cert
}

func (pr *PeerRegistry) GetPeerCertificate(peerID string) (*x509.Certificate, bool) {
	pr.peerCertsMu.RLock()
	defer pr.peerCertsMu.RUnlock()
	cert, exists := pr.peerCerts[peerID]
	return cert, exists
}

