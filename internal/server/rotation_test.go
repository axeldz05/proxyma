package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestServerTLSALPNCallbackUsesAllowlist(t *testing.T) {
	t.Parallel()

	srv := NewServer(t, testutil.DefaultConfig(t, "tls-alpn"), nil)
	callback := srv.ServerTLSConfig().GetConfigForClient
	require.NotNil(t, callback)

	cfg, err := callback(&tls.ClientHelloInfo{
		SupportedProtos: []string{"attacker-protocol", "h2", "proxyma-p2p"},
	})
	require.NoError(t, err)
	require.NotContains(t, cfg.NextProtos, "attacker-protocol")
	require.ElementsMatch(t, []string{"h2", "proxyma-p2p"}, cfg.NextProtos)
}

func TestClusterCARotation(t *testing.T) {
	// 1. Initialize Sponsor node
	sponsorCfg := testutil.DefaultConfig(t, "sponsor")
	isSponsorTrue := true
	sponsorCfg.IsSponsorOverride = &isSponsorTrue
	sponsorSrv := NewServer(t, sponsorCfg, nil)

	// 2. Initialize two Peer nodes (non-sponsors)
	peer1Cfg := testutil.DefaultConfig(t, "peer1")
	isSponsorFalse := false
	peer1Cfg.IsSponsorOverride = &isSponsorFalse
	peer1Srv := NewServer(t, peer1Cfg, nil)

	peer2Cfg := testutil.DefaultConfig(t, "peer2")
	peer2Cfg.IsSponsorOverride = &isSponsorFalse
	peer2Srv := NewServer(t, peer2Cfg, nil)

	// 3. Interconnect them
	sponsorSrv.AddPeer(peer1Srv.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer1Srv.Config.Address},
		IsSponsor: false,
	})
	sponsorSrv.AddPeer(peer2Srv.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer2Srv.Config.Address},
		IsSponsor: false,
	})

	peer1Srv.AddPeer(sponsorSrv.Config.ID, protocol.AddressRecord{
		Addresses: []string{sponsorSrv.Config.Address},
		IsSponsor: true,
	})
	peer1Srv.AddPeer(peer2Srv.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer2Srv.Config.Address},
		IsSponsor: false,
	})

	peer2Srv.AddPeer(sponsorSrv.Config.ID, protocol.AddressRecord{
		Addresses: []string{sponsorSrv.Config.Address},
		IsSponsor: true,
	})
	peer2Srv.AddPeer(peer1Srv.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer1Srv.Config.Address},
		IsSponsor: false,
	})

	// 4. Register initial peer certificates programmatically (mTLS simulation)
	sponsorCertRaw := sponsorSrv.ServerTLSConfig().Certificates[0].Certificate[0]
	sponsorCert, err := x509.ParseCertificate(sponsorCertRaw)
	require.NoError(t, err)

	peer1CertRaw := peer1Srv.ServerTLSConfig().Certificates[0].Certificate[0]
	peer1Cert, err := x509.ParseCertificate(peer1CertRaw)
	require.NoError(t, err)

	peer2CertRaw := peer2Srv.ServerTLSConfig().Certificates[0].Certificate[0]
	peer2Cert, err := x509.ParseCertificate(peer2CertRaw)
	require.NoError(t, err)

	sponsorSrv.Peers.SetPeerCertificate(peer1Srv.Config.ID, peer1Cert)
	sponsorSrv.Peers.SetPeerCertificate(peer2Srv.Config.ID, peer2Cert)

	peer1Srv.Peers.SetPeerCertificate(sponsorSrv.Config.ID, sponsorCert)
	peer1Srv.Peers.SetPeerCertificate(peer2Srv.Config.ID, peer2Cert)

	peer2Srv.Peers.SetPeerCertificate(sponsorSrv.Config.ID, sponsorCert)
	peer2Srv.Peers.SetPeerCertificate(peer1Srv.Config.ID, peer1Cert)

	// Mark them online
	sponsorSrv.SetPeerOnline(peer1Srv.Config.ID, true)
	sponsorSrv.SetPeerOnline(peer2Srv.Config.ID, true)
	peer1Srv.SetPeerOnline(sponsorSrv.Config.ID, true)
	peer1Srv.SetPeerOnline(peer2Srv.Config.ID, true)
	peer2Srv.SetPeerOnline(sponsorSrv.Config.ID, true)
	peer2Srv.SetPeerOnline(peer1Srv.Config.ID, true)

	// 5. Verify initial connection works via mTLS
	{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		manifest, err := sponsorSrv.PeerClient().FetchManifest(ctx, peer1Srv.Config.ID)
		require.NoError(t, err)
		require.NotNil(t, manifest)
	}

	// 6. Trigger CA rotation on the Sponsor node
	sponsorSrv.RotateCAAndResignPeers()

	// 7. Verify connectivity works after rotation under the new dynamic CA
	// Use eventually or simple retry logic because re-sign is done asynchronously.
	// Wait, RotateCAAndResignPeers waits for all peer push goroutines to finish (wg.Wait())!
	// So rotation is completely done when RotateCAAndResignPeers returns.
	{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		manifest, err := sponsorSrv.PeerClient().FetchManifest(ctx, peer1Srv.Config.ID)
		require.NoError(t, err)
		require.NotNil(t, manifest)

		// Verify peer-to-peer mTLS communication still works
		manifestP2P, err := peer1Srv.PeerClient().FetchManifest(ctx, peer2Srv.Config.ID)
		require.NoError(t, err)
		require.NotNil(t, manifestP2P)
	}

	// 8. Verify that a push request from a non-Sponsor is rejected
	{
		payload := map[string]string{
			"ca_cert":   "fake-ca",
			"node_cert": "fake-node-cert",
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)

		req, err := http.NewRequest("POST", peer2Srv.Config.Address+protocol.PathClusterRotate, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		// Make request from peer1 (non-sponsor) to peer2
		resp, err := peer1Srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode, "Push from non-sponsor must be forbidden")
	}
}

func TestCARotationReloadsQUICTLS(t *testing.T) {
	// Not parallel: RotateCA mutates shared certs-dir layout; keep sequential with TestClusterCARotation.
	sponsorCfg := testutil.DefaultConfig(t, "sponsor-quic-rot")
	isSponsorTrue := true
	sponsorCfg.IsSponsorOverride = &isSponsorTrue
	sponsorSrv := NewServer(t, sponsorCfg, nil)

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = udp.Close() })

	qm := p2p.NewQUICManager(
		sponsorCfg.ID, udp,
		sponsorSrv.ClientTLSConfig(), sponsorSrv.ServerTLSConfig(),
		sponsorSrv.MountHandlers(), sponsorCfg.Logger,
	)
	qm.SetPublicUDPAddr(udp.LocalAddr().String())
	require.NoError(t, qm.StartListener())
	t.Cleanup(qm.Close)
	sponsorSrv.AttachQUICManager(qm)

	listenerBefore := qm.QUICListener
	oldServerDER := append([]byte(nil), qm.TLSServer.Certificates[0].Certificate[0]...)
	oldClientDER := append([]byte(nil), qm.TLSClient.Certificates[0].Certificate[0]...)

	sponsorSrv.RotateCAAndResignPeers()

	qmAfter := sponsorSrv.QUICManager()
	require.NotNil(t, qmAfter)
	require.Same(t, listenerBefore, qmAfter.QUICListener, "TLS rotation must not need to restart the live listener")
	require.NotEmpty(t, qmAfter.TLSServer.Certificates)
	require.NotEmpty(t, qmAfter.TLSClient.Certificates)

	// HTTP configs use atomic snapshots — compare QUIC material against a fresh
	// LoadNodeTLS from the live server config CA path after rotation.
	caPath := sponsorSrv.Config.CAPath
	certPath, keyPath := p2p.NodeCertPaths(filepath.Dir(caPath), sponsorSrv.Config.ID)
	stls, ctls, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	require.NoError(t, err)

	require.Equal(t, stls.Certificates[0].Certificate[0], qmAfter.TLSServer.Certificates[0].Certificate[0],
		"QUIC server TLS must match rotated disk server TLS")
	require.Equal(t, ctls.Certificates[0].Certificate[0], qmAfter.TLSClient.Certificates[0].Certificate[0],
		"QUIC client TLS must match rotated disk client TLS")
	require.NotEqual(t, oldServerDER, qmAfter.TLSServer.Certificates[0].Certificate[0])
	require.NotEqual(t, oldClientDER, qmAfter.TLSClient.Certificates[0].Certificate[0])

	// AUD-003 claimed the live listener kept serving its pre-rotation TLS
	// material. Prove QUICManager's generation-checked callback instead reads
	// the rotated snapshot during a real inbound QUIC handshake.
	rotatedClient := testutil.IssueNode(t, filepath.Dir(caPath), t.TempDir(), "rotated-quic-client")
	clientTLS := rotatedClient.ClientTLS.Clone()
	clientTLS.NextProtos = []string{"proxyma-p2p"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, qmAfter.PacketConn.LocalAddr().String(), clientTLS, &quic.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test complete") })

	peerCert := conn.ConnectionState().TLS.PeerCertificates[0]
	require.Equal(t, stls.Certificates[0].Certificate[0], peerCert.Raw,
		"the existing listener must present the rotated server certificate")
	require.NotEqual(t, oldServerDER, peerCert.Raw)
	require.Eventually(t, func() bool {
		_, ok := qmAfter.GetSession(rotatedClient.ID)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "server must accept the rotated client certificate")
}
