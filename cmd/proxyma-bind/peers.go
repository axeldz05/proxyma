package proxyma_bind

import (
	"encoding/json"
	"fmt"
)

// GetPeersJson returns active peers.
func GetPeersJson() string {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()

	if s == nil {
		data, err := sendUnixSocketCommand(appStorage, "peers", nil)
		if err != nil {
			return fmt.Sprintf(`{"error": %q}`, err.Error())
		}
		return string(data)
	}

	list := s.LocalPeersList()
	b, _ := json.Marshal(list)
	return string(b)
}
