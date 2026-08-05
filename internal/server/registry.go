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
	peerErrors        map[string]string
	peerErrorsMu      sync.RWMutex
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
		peerErrors:      make(map[string]string),
		clusterServices: make(map[string]map[string]protocol.ServiceSchema),
		peerCerts:       make(map[string]*x509.Certificate),
	}
}

// AddPeer adds or updates a peer's address record. It returns true if the peer record was actually updated.
func (pr *PeerRegistry) AddPeer(peerID string, addressRecord protocol.AddressRecord) bool {
	if peerID == pr.nodeID {
		return false
	}

	pr.peersMu.Lock()
	defer pr.peersMu.Unlock()

	// Clean up stale peers with the same primary address
	if len(addressRecord.Addresses) > 0 {
		newPrimaryAddr := addressRecord.Addresses[0]
		var staleIDs []string
		for id, existingRecord := range pr.peers {
			if id != peerID && len(existingRecord.Addresses) > 0 && existingRecord.Addresses[0] == newPrimaryAddr {
				staleIDs = append(staleIDs, id)
			}
		}

		if len(staleIDs) > 0 {
			for _, staleID := range staleIDs {
				delete(pr.peers, staleID)
			}
			for _, staleID := range staleIDs {
				pr.purgePeerMaps(staleID)
			}
			for _, staleID := range staleIDs {
				pr.logger.Info("Removing stale peer replaced by new peer ID at same address", "stalePeerID", staleID, "newPeerID", peerID, "address", newPrimaryAddr)
			}
		}
	}

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
	// SetPeerOnline will acquire activePeersMu.Lock() internally, which is safe since we lock in the correct order
	pr.activePeersMu.Lock()
	pr.activePeers[peerID] = true
	pr.activePeersMu.Unlock()

	pr.peerErrorsMu.Lock()
	delete(pr.peerErrors, peerID)
	pr.peerErrorsMu.Unlock()

	pr.logger.Info("peerID added to peers", "peerID", peerID, "node", pr.nodeID)
	return true
}

// purgePeerLocked removes peerID from all registry maps. Callers must hold peersMu when deleting from peers;
// other map locks are taken internally.
func (pr *PeerRegistry) purgePeerMaps(peerID string) {
	pr.activePeersMu.Lock()
	delete(pr.activePeers, peerID)
	pr.activePeersMu.Unlock()

	pr.peerErrorsMu.Lock()
	delete(pr.peerErrors, peerID)
	pr.peerErrorsMu.Unlock()

	pr.clusterServicesMu.Lock()
	delete(pr.clusterServices, peerID)
	pr.clusterServicesMu.Unlock()

	pr.peerCertsMu.Lock()
	delete(pr.peerCerts, peerID)
	pr.peerCertsMu.Unlock()
}

// RemovePeer removes a peer and its status/services from the registry.
func (pr *PeerRegistry) RemovePeer(peerID string) {
	pr.peersMu.Lock()
	delete(pr.peers, peerID)
	pr.peersMu.Unlock()

	pr.purgePeerMaps(peerID)

	pr.logger.Info("peerID removed from peers", "peerID", peerID)
}

// SetPeerOnline marks a peer as online or offline.
func (pr *PeerRegistry) SetPeerOnline(peerID string, online bool) {
	if !online {
		pr.SetPeerOffline(peerID, nil)
		return
	}
	pr.activePeersMu.Lock()
	pr.activePeers[peerID] = true
	pr.activePeersMu.Unlock()

	pr.peerErrorsMu.Lock()
	delete(pr.peerErrors, peerID)
	pr.peerErrorsMu.Unlock()
}

// SetPeerOffline marks a peer as offline and stores the connection error.
func (pr *PeerRegistry) SetPeerOffline(peerID string, err error) {
	pr.activePeersMu.Lock()
	pr.activePeers[peerID] = false
	pr.activePeersMu.Unlock()

	pr.peerErrorsMu.Lock()
	if err != nil {
		pr.peerErrors[peerID] = "offline or could not reach: " + err.Error()
	} else {
		pr.peerErrors[peerID] = "offline"
	}
	pr.peerErrorsMu.Unlock()
}

// GetPeerError retrieves the connection error for a specific peer.
func (pr *PeerRegistry) GetPeerError(peerID string) string {
	pr.peerErrorsMu.RLock()
	defer pr.peerErrorsMu.RUnlock()
	return pr.peerErrors[peerID]
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

// GetServiceSchema searches all peers for the given service schema.
func (pr *PeerRegistry) GetServiceSchema(serviceName string) (protocol.ServiceSchema, bool) {
	pr.clusterServicesMu.RLock()
	defer pr.clusterServicesMu.RUnlock()
	for _, peerServices := range pr.clusterServices {
		if schema, ok := peerServices[serviceName]; ok {
			return schema, true
		}
	}
	return protocol.ServiceSchema{}, false
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
