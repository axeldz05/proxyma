package server

import (
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"time"
)

type LocalService = protocol.LocalService

func (s *Server) LocalInviteGenerate(validForMinutes int) (string, error) {
	if validForMinutes <= 0 {
		validForMinutes = 15
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
	if err != nil {
		return "", err
	}
	expiration := time.Now().Add(time.Duration(validForMinutes) * time.Minute)
	s.AddPendingInvite(secretHex, expiration)
	return smartToken, nil
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
