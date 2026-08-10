package proxyma_bind

// GetPeersJson returns active peers.
func GetPeersJson() string {
	return InvokeDomainAction("peers", "list", nil)
}
