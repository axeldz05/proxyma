package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"strings"
	"sync"
	"time"
)

func (s *Server) SetTLSConfigs(serverTLS, clientTLS *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.serverTLSConfig = serverTLS
	s.clientTLSConfig = clientTLS
}

func (s *Server) ReloadTLSConfig(caPath, certPath, keyPath string) error {
	newServerTLS, newClientTLS, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	if err != nil {
		return fmt.Errorf("failed to load rotated TLS certs: %w", err)
	}

	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()

	s.Config.Logger.Info("Reloading dynamic TLS configuration across server and client...")

	if s.serverTLSConfig != nil {
		s.serverTLSConfig.Certificates = newServerTLS.Certificates
		s.serverTLSConfig.ClientCAs = newServerTLS.ClientCAs
	}

	if s.clientTLSConfig != nil {
		s.clientTLSConfig.Certificates = newClientTLS.Certificates
		s.clientTLSConfig.RootCAs = newClientTLS.RootCAs
		s.clientTLSConfig.VerifyPeerCertificate = newClientTLS.VerifyPeerCertificate
	}

	return nil
}

func (s *Server) RotateCAAndResignPeers() {
	caKeyPath := strings.Replace(s.Config.CAPath, ".crt", ".key", 1)
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
	ownCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", s.Config.ID))
	ownKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", s.Config.ID))
	err = p2p.IssueNodeCertificate(certsDir, certsDir, s.Config.ID)
	if err != nil {
		s.Config.Logger.Error("Failed to re-sign own certificate", "error", err)
		return
	}

	// 3. Loop over all other registered peers and re-sign their certificates
	peers := s.GetPeersCopy()
	var wg sync.WaitGroup

	for peerID, addr := range peers {
		if peerID == s.Config.ID {
			continue
		}

		cert, hasCert := s.Peers.GetPeerCertificate(peerID)
		if !hasCert {
			s.Config.Logger.Warn("No client certificate cached for peer, cannot re-sign. They must re-join.", "peerID", peerID)
			continue
		}

		wg.Add(1)
		go func(pid, paddr string, pcert *x509.Certificate) {
			defer wg.Done()

			newCertPEM, err := p2p.ReSignPeerCertificate(pcert.PublicKey, pid, s.Config.CAPath, caKeyPath)
			if err != nil {
				s.Config.Logger.Error("Failed to re-sign peer certificate", "peerID", pid, "error", err)
				return
			}

			caCertPEM, err := os.ReadFile(s.Config.CAPath)
			if err != nil {
				return
			}

			rotationPayload := map[string]string{
				"ca_cert":   string(caCertPEM),
				"node_cert": string(newCertPEM),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err = s.peerClient.RotateTLS(ctx, pid, rotationPayload)
			if err != nil {
				s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", pid, "error", err)
			} else {
				s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", pid)
			}
		}(peerID, addr, cert)
	}

	wg.Wait()

	// 4. Finally, reload our own TLS config in place
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}
