package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
)

func (s *Server) SetTLSConfigs(serverTLS, clientTLS *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.serverTLSConfig = serverTLS
	s.clientTLSConfig = clientTLS
}

// armHotReloadServerTLS installs GetConfigForClient so ServeTLS's cloned config
// still picks up rotated certs/CA from disk on every handshake (L2).
func (s *Server) armHotReloadServerTLS(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		caPath := s.Config.CAPath
		certPath, keyPath := p2p.ResolveNodeCertPaths(caPath, s.Config.StoragePath, s.Config.ID)
		stls, _, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
		if err != nil {
			return nil, err
		}
		return stls, nil
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

	// Server side: ServeTLS clones TLSConfig; armHotReloadServerTLS makes handshakes
	// reload from disk. Still refresh the base pointer for httptest / non-cloned paths.
	if s.serverTLSConfig != nil {
		s.serverTLSConfig.Certificates = newServerTLS.Certificates
		s.serverTLSConfig.ClientCAs = newServerTLS.ClientCAs
		s.armHotReloadServerTLS(s.serverTLSConfig)
	}

	// Client side is held by HTTP transports by pointer (not cloned) — mutate in place.
	if s.clientTLSConfig != nil {
		s.clientTLSConfig.Certificates = newClientTLS.Certificates
		s.clientTLSConfig.RootCAs = newClientTLS.RootCAs
		s.clientTLSConfig.VerifyPeerCertificate = newClientTLS.VerifyPeerCertificate
		s.clientTLSConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			_, ctls, loadErr := p2p.LoadNodeTLS(caPath, certPath, keyPath)
			if loadErr != nil {
				return nil, loadErr
			}
			if len(ctls.Certificates) == 0 {
				return nil, fmt.Errorf("no client certificate available after reload")
			}
			return &ctls.Certificates[0], nil
		}
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

	// 3. Loop over all other registered peers and re-sign their certificates
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

		rotationPayload := map[string]string{
			"ca_cert":   string(caCertPEM),
			"node_cert": string(newCertPEM),
		}

		err = s.peerClient.RotateTLS(ctx, peerID, rotationPayload)
		if err != nil {
			s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", peerID, "error", err)
			return err
		}
		s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", peerID)
		return nil
	})

	// 4. Finally, reload our own TLS config in place
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}
