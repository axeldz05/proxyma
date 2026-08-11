package server

import (
	"errors"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"time"
)

var errNotCAAuthority = errors.New("only the CA authority node can mint invites")

// LocalInviteGenerate creates an invite token and returns it with its expiry (SSOT).
// Only the node that holds ca.key may mint invites (cluster expansion privilege).
func (s *Server) LocalInviteGenerate(validForMinutes int) (token string, expires time.Time, err error) {
	if !s.hasCAKey() {
		return "", time.Time{}, errNotCAAuthority
	}
	if validForMinutes <= 0 {
		validForMinutes = protocol.DefaultInviteMinutes
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
	if err != nil {
		return "", time.Time{}, err
	}
	expiration := time.Now().Add(time.Duration(validForMinutes) * time.Minute)
	s.AddPendingInvite(secretHex, expiration)
	return smartToken, expiration, nil
}

func (s *Server) LocalBandwidthStats() protocol.BandwidthStats {
	upSpeed, downSpeed := s.GetCurrentBandwidth()
	totalSent, totalRecv := s.GetTotalBandwidth()
	return protocol.BandwidthStats{
		UploadSpeed:   int64(upSpeed),
		DownloadSpeed: int64(downSpeed),
		TotalSent:     totalSent,
		TotalReceived: totalRecv,
	}
}

func (s *Server) LocalPeersList() []protocol.PeerStatus {
	var list []protocol.PeerStatus
	for id, addr := range s.GetPeersCopy() {
		online := s.IsPeerOnline(id)
		var errMsg string
		if !online {
			errMsg = s.Peers.GetPeerError(id)
		}
		list = append(list, protocol.PeerStatus{
			ID:      id,
			Address: addr,
			Online:  online,
			Error:   errMsg,
		})
	}
	return list
}
