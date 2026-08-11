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

func (s *Server) SetPeerOnline(peerID string, online bool) {
	s.Peers.SetPeerOnline(peerID, online)
}

func (s *Server) SetPeerOffline(peerID string, err error) {
	s.Peers.SetPeerOffline(peerID, err)
}

func (s *Server) IsPeerOnline(peerID string) bool {
	return s.Peers.IsPeerOnline(peerID)
}

func (s *Server) RemovePeer(peerID string) {
	s.Peers.RemovePeer(peerID)
	if s.Storage != nil {
		_ = s.Storage.DeletePeer(peerID)
	}
	s.peerClient.RemovePeerRoute(peerID)
}

func (s *Server) announceOffline(ctx context.Context) {
	payload := protocol.PeerIDRequest{ID: s.Config.ID}
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCShort, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) error {
		return s.peerClient.Offline(ctx, peerID, payload)
	})
}

func (s *Server) GetClusterServices(peerID string) map[string]protocol.ServiceSchema {
	return s.Peers.GetClusterServices(peerID)
}

func (s *Server) SetAddress(addr string) {
	s.Config.Address = addr
	s.Compute.SetAddress(addr)
}

func (s *Server) AddPeer(peerID string, addressRecord protocol.AddressRecord) {
	updated, evicted := s.Peers.AddPeer(peerID, addressRecord)
	for _, id := range evicted {
		if s.Storage != nil {
			_ = s.Storage.DeletePeer(id)
		}
		if s.peerClient != nil {
			s.peerClient.RemovePeerRoute(id)
		}
	}
	if updated {
		if s.Storage != nil {
			if err := s.Storage.SavePeer(peerID, addressRecord); err != nil {
				s.Config.Logger.Error("Failed to save peer to DB", "peerID", peerID, "error", err)
			}
		}
		if s.peerClient != nil {
			s.peerClient.UpdatePeerRoute(peerID, addressRecord)
		}
		go s.syncCatalogToPeer(peerID)
	}
}

func (s *Server) GetPeersCopy() map[string]string {
	return s.Peers.GetPeersCopy()
}

func (s *Server) GetSponsorPeers() map[string]string {
	return s.Peers.GetSponsorPeers()
}

func (s *Server) AnnouncePresence(sponsorAddress string) error {
	s.CheckNAT()
	tcpPortStr := s.advertisedTCPPort()
	addresses := []string{s.Config.Address}
	seen := map[string]bool{s.Config.Address: true}
	// LAN/container IPs as fallbacks when node-ID DNS is unavailable (bare metal).
	if ips, err := utils.GetLocalIPs(); err == nil {
		for _, ip := range ips {
			if ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			addr := protocol.HTTPSAddr(ip.String(), tcpPortStr)
			if !seen[addr] {
				addresses = append(addresses, addr)
				seen[addr] = true
			}
		}
	}
	if s.isSponsor && s.publicUDPAddr != "" {
		host, _, err := net.SplitHostPort(s.publicUDPAddr)
		if err == nil {
			publicTCPAddr := protocol.HTTPSAddr(host, tcpPortStr)
			if !seen[publicTCPAddr] {
				addresses = append(addresses, publicTCPAddr)
				seen[publicTCPAddr] = true
			}
		}
	}
	if s.publicUDPAddr != "" {
		addresses = append(addresses, p2p.FormatQUICAddr(s.publicUDPAddr))
	}
	payload := protocol.AddPeerRequest{
		ID: s.Config.ID,
		Address: protocol.AddressRecord{
			Addresses: addresses,
			IsSponsor: s.isSponsor,
		},
	}

	announceResp, err := s.peerClient.Announce(sponsorAddress, payload)
	if err != nil {
		s.Config.Logger.Error("Error while announcing from sponsor", "sponsor", sponsorAddress, "error", err)
		return fmt.Errorf("there was an error trying to connect to the cluster: %v", err)
	}
	s.Config.Logger.Info("AnnounceResp received without errors", "resp", announceResp)
	for id, addrRec := range announceResp {
		if id != s.Config.ID {
			s.AddPeer(id, addrRec)
		}
	}
	s.Config.Logger.Info("Successfully synced topology from sponsor", "peers_count", len(announceResp))
	go func() {
		_ = s.ExecuteSync()
	}()
	return nil
}

func (s *Server) CheckNAT() {
	s.checkNATOnce.Do(func() {
		s.determineSponsorAndNATStatus()
	})
}

func (s *Server) AddPendingInvite(secret string, expiration time.Time) {
	s.Invites.Add(secret, expiration)
}

func (s *Server) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return s.peerClient.DiscoverServices(ctx, peerID)
}

func (s *Server) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	return s.Peers.GetPeerRecord(peerID)
}

func (s *Server) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	return s.Peers.GetPeersRecordCopy()
}
