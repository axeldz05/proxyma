package p2p

import (
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBlockedPeerCannotRepublishSessionUntilAllowed(t *testing.T) {
	t.Parallel()

	qm := &QUICManager{Sessions: make(map[string]*quic.Conn)}
	qm.BlockPeerSessions("removed", 0, "removed")
	require.False(t, qm.setSession("removed", nil, nil))
	_, exists := qm.GetSession("removed")
	require.False(t, exists)

	qm.AllowPeerSessions("removed")
	require.True(t, qm.setSession("removed", nil, nil))
	_, exists = qm.GetSession("removed")
	require.True(t, exists)

	qm.SessionsMu.Lock()
	delete(qm.Sessions, "removed")
	qm.SessionsMu.Unlock()
}
