package proxyma_bind

import (
	"proxyma/internal/server"
)

// GetPeersJson returns active peers.
func GetPeersJson() string {
	return dispatchUnixOrLocal("peers", nil, func(s *server.Server) (any, error) {
		return s.LocalPeersList(), nil
	})
}
