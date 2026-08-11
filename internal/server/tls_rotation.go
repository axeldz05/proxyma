package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
)

// tlsClientMaterial is an immutable client cert + verify pair swapped on rotation (H1).
type tlsClientMaterial struct {
	cert   tls.Certificate
	verify func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error
}

// tlsServerMaterial is an immutable server tls.Config snapshot swapped on rotation (H1).
type tlsServerMaterial struct {
	cfg *tls.Config
}

func (s *Server) SetTLSConfigs(serverTLS, clientTLS *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.serverTLSConfig = serverTLS
	s.clientTLSConfig = clientTLS
	// Arm callbacks once before any handshake; never mutate tls.Config fields later.
	s.armHotReloadServerTLSLocked(serverTLS)
	s.armHotReloadClientTLSLocked(clientTLS)
}

// armHotReloadServerTLS installs GetConfigForClient so ServeTLS's cloned config
// still picks up rotated certs/CA from an atomic snapshot (L2).
func (s *Server) armHotReloadServerTLS(cfg *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.armHotReloadServerTLSLocked(cfg)
}

func (s *Server) armHotReloadServerTLSLocked(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	s.storeServerMaterial(cfg)
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		m := s.serverMaterial.Load()
		if m == nil || m.cfg == nil {
			return nil, fmt.Errorf("no server TLS material")
		}
		stls := m.cfg.Clone()
		// Honor ALPN the client offered (HTTP/2 or QUIC proxyma-p2p).
		if hello != nil && len(hello.SupportedProtos) > 0 {
			stls.NextProtos = append([]string(nil), hello.SupportedProtos...)
		}
		return stls, nil
	}
}

func (s *Server) storeServerMaterial(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	// Clone without GetConfigForClient to avoid recursive callback on Clone use.
	snap := cfg.Clone()
	snap.GetConfigForClient = nil
	s.serverMaterial.Store(&tlsServerMaterial{cfg: snap})
}

func materialFromClientTLS(cfg *tls.Config) *tlsClientMaterial {
	if cfg == nil {
		return nil
	}
	m := &tlsClientMaterial{verify: cfg.VerifyPeerCertificate}
	if len(cfg.Certificates) > 0 {
		m.cert = cfg.Certificates[0]
	}
	return m
}

// armHotReloadClientTLSLocked installs cert/verify callbacks that read an atomic
// snapshot. ReloadTLSConfig swaps the snapshot — it never mutates cfg fields (H1).
func (s *Server) armHotReloadClientTLSLocked(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	if m := materialFromClientTLS(cfg); m != nil && len(m.cert.Certificate) > 0 {
		s.clientMaterial.Store(m)
	}
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		m := s.clientMaterial.Load()
		if m == nil || len(m.cert.Certificate) == 0 {
			return nil, fmt.Errorf("no client certificate available")
		}
		cert := m.cert
		return &cert, nil
	}
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		m := s.clientMaterial.Load()
		if m == nil || m.verify == nil {
			return fmt.Errorf("no client TLS verify material")
		}
		return m.verify(rawCerts, verifiedChains)
	}
}

func (s *Server) ReloadTLSConfig(caPath, certPath, keyPath string) error {
	newServerTLS, newClientTLS, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	if err != nil {
		return fmt.Errorf("failed to load rotated TLS certs: %w", err)
	}

	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()

	s.Config.Logger.Info("Reloading dynamic TLS configuration across server and client...")

	// Swap snapshots only — do not write Certificates/ClientCAs/RootCAs on live configs.
	s.storeServerMaterial(newServerTLS)
	if m := materialFromClientTLS(newClientTLS); m != nil {
		s.clientMaterial.Store(m)
	}

	if s.quicMgr != nil {
		s.quicMgr.ReloadTLS(newClientTLS, newServerTLS)
	}

	return nil
}

func (s *Server) RotateCAAndResignPeers() {
	caKeyPath := p2p.CAKeyPath(s.Config.CAPath)
	if _, err := os.Stat(caKeyPath); err != nil {
		s.Config.Logger.Debug("We are not the CA authority node, skipping CA rotation")
		return
	}

	s.Config.Logger.Info("Triggering CA Rotation & Peer Re-signing...")

	// 1. Rotate the CA files
	certsDir := filepath.Dir(s.Config.CAPath)
	err := p2p.RotateCA(certsDir)
	if err != nil {
		s.Config.Logger.Error("Failed to rotate CA", "error", err)
		return
	}

	// 2. Re-sign own certificate with the new CA
	ownCertFile, ownKeyFile := p2p.NodeCertPaths(certsDir, s.Config.ID)
	err = p2p.IssueNodeCertificate(certsDir, certsDir, s.Config.ID)
	if err != nil {
		s.Config.Logger.Error("Failed to re-sign own certificate", "error", err)
		return
	}

	// 3. Push re-signed peer certs. Client/server snapshots stay on the pre-rotation
	// material until step 4 so peers still presenting old CA certs can be reached.
	caCertPEM, err := p2p.ReadCAPEM(s.Config.CAPath)
	if err != nil {
		s.Config.Logger.Error("Failed to read new CA cert", "error", err)
		return
	}

	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCSync, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) error {
		cert, hasCert := s.Peers.GetPeerCertificate(peerID)
		if !hasCert {
			s.Config.Logger.Warn("No client certificate cached for peer, cannot re-sign. They must re-join.", "peerID", peerID)
			return nil
		}

		newCertPEM, err := p2p.ReSignPeerCertificate(cert.PublicKey, peerID, s.Config.CAPath, caKeyPath)
		if err != nil {
			s.Config.Logger.Error("Failed to re-sign peer certificate", "peerID", peerID, "error", err)
			return err
		}

		err = s.peerClient.RotateTLS(ctx, peerID, protocol.RotateTLSPayload{
			CACert:   string(caCertPEM),
			NodeCert: string(newCertPEM),
		})
		if err != nil {
			s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", peerID, "error", err)
			return err
		}
		s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", peerID)
		return nil
	})

	// 4. Finally, reload our own TLS snapshots
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}
