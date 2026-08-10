package server_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
	qm.PublicUDPAddr = udp.LocalAddr().String()
	require.NoError(t, qm.StartListener())
	t.Cleanup(qm.Close)
	sponsorSrv.AttachQUICManager(qm)

	oldServerDER := append([]byte(nil), qm.TLSServer.Certificates[0].Certificate[0]...)
	oldClientDER := append([]byte(nil), qm.TLSClient.Certificates[0].Certificate[0]...)

	sponsorSrv.RotateCAAndResignPeers()

	qmAfter := sponsorSrv.QUICManager()
	require.NotNil(t, qmAfter)
	require.NotEmpty(t, qmAfter.TLSServer.Certificates)
	require.NotEmpty(t, qmAfter.TLSClient.Certificates)

	httpServerDER := sponsorSrv.ServerTLSConfig().Certificates[0].Certificate[0]
	httpClientDER := sponsorSrv.ClientTLSConfig().Certificates[0].Certificate[0]

	require.Equal(t, httpServerDER, qmAfter.TLSServer.Certificates[0].Certificate[0],
		"QUIC server TLS must match rotated HTTP server TLS")
	require.Equal(t, httpClientDER, qmAfter.TLSClient.Certificates[0].Certificate[0],
		"QUIC client TLS must match rotated HTTP client TLS")
	require.NotEqual(t, oldServerDER, qmAfter.TLSServer.Certificates[0].Certificate[0])
	require.NotEqual(t, oldClientDER, qmAfter.TLSClient.Certificates[0].Certificate[0])
}
