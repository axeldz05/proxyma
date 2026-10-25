package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"sync"
	"time"
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

var serverALPNAllowlist = []string{"h2", "http/1.1", "proxyma-p2p"}

func allowedServerALPN(offered []string) []string {
	if len(offered) == 0 {
		return nil
	}
	offeredSet := make(map[string]struct{}, len(offered))
	protocol.PopulateMap(offeredSet, offered, struct{}{})
	allowed := make([]string, 0, len(serverALPNAllowlist))
	for _, proto := range serverALPNAllowlist {
		if _, ok := offeredSet[proto]; ok {
			allowed = append(allowed, proto)
		}
	}
	return allowed
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

// armHotReloadServerTLS installs GetConfigForClient so HTTP listener snapshots
// pick up rotated certs/CA per handshake. QUICManager owns an equivalent
// generation-checked callback for its live listener (L2).
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
		stls.NextProtos = nil
		if hello != nil {
			stls.NextProtos = allowedServerALPN(hello.SupportedProtos)
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

	if qm := s.CurrentNATState().QUICManager; qm != nil {
		qm.ReloadTLS(newClientTLS, newServerTLS)
	}

	return nil
}

func (s *Server) RotateCAAndResignPeers() {
	if s.lifetimeCtx.Err() != nil {
		return
	}
	caKeyPath := p2p.CAKeyPath(s.Config.CAPath)
	if _, err := os.Stat(caKeyPath); err != nil {
		s.Config.Logger.Debug("We are not the CA authority node, skipping CA rotation")
		return
	}

	s.Config.Logger.Info("Triggering CA Rotation & Peer Re-signing...")

	// Stage new CA and node certificate in a temporary directory
	stageDir, err := os.MkdirTemp("", "proxyma-ca-rotation-*")
	if err != nil {
		s.Config.Logger.Error("Failed to create CA rotation staging directory", "error", err)
		return
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	// 1. Generate new CA in staging directory
	err = p2p.RotateCA(stageDir)
	if err != nil {
		s.Config.Logger.Error("Failed to rotate CA in staging", "error", err)
		return
	}
	stageCAPath, stageCAKeyPath := p2p.CACertPaths(stageDir)
	stageOwnCertFile, stageOwnKeyFile := p2p.NodeCertPaths(stageDir, s.Config.ID)

	// 2. Issue own node certificate using staged CA
	err = p2p.IssueNodeCertificate(stageDir, stageDir, s.Config.ID)
	if err != nil {
		s.Config.Logger.Error("Failed to issue staged own certificate", "error", err)
		return
	}

	newCACertPEM, err := p2p.ReadCAPEM(stageCAPath)
	if err != nil {
		s.Config.Logger.Error("Failed to read staged CA cert", "error", err)
		return
	}
	oldCACertPEM, err := p2p.ReadCAPEM(s.Config.CAPath)
	if err != nil {
		s.Config.Logger.Error("Failed to read current CA cert", "error", err)
		return
	}

	// Install transitional in-memory trust bundle (old CA + new CA) so pre-rotation
	// and post-rotation peers can both be accepted and dialed during transition.
	transitionalPool := x509.NewCertPool()
	transitionalPool.AppendCertsFromPEM(oldCACertPEM)
	transitionalPool.AppendCertsFromPEM(newCACertPEM)

	s.tlsMutex.Lock()
	oldServerMaterial := s.serverMaterial.Load()
	oldClientMaterial := s.clientMaterial.Load()

	if oldServerMaterial != nil && oldServerMaterial.cfg != nil {
		transitionalServerCfg := oldServerMaterial.cfg.Clone()
		transitionalServerCfg.ClientCAs = transitionalPool
		s.storeServerMaterial(transitionalServerCfg)
	}

	if oldClientMaterial != nil {
		transitionalClientMaterial := &tlsClientMaterial{
			cert: oldClientMaterial.cert,
			verify: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("no certificates provided by peer")
				}
				cert, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return fmt.Errorf("failed to parse certificate: %w", err)
				}
				opts := x509.VerifyOptions{
					Roots:       transitionalPool,
					CurrentTime: time.Now(),
				}
				if _, err := cert.Verify(opts); err != nil {
					return fmt.Errorf("bad certificate: %w", err)
				}
				return nil
			},
		}
		s.clientMaterial.Store(transitionalClientMaterial)
	}
	s.tlsMutex.Unlock()

	// 3. Push re-signed peer certs to all peers.
	var (
		pushMu   sync.Mutex
		pushFail int
	)
	s.forEachPeer(forEachPeerOpts{Context: s.lifetimeCtx, Timeout: PeerRPCSync, Parallel: true, SkipSelf: true}, func(ctx context.Context, peerID string) error {
		cert, hasCert := s.Peers.GetPeerCertificate(peerID)
		if !hasCert {
			s.Config.Logger.Error("No client certificate cached for peer; aborting local TLS reload", "peerID", peerID)
			pushMu.Lock()
			pushFail++
			pushMu.Unlock()
			return errPeerSkipped
		}

		newCertPEM, err := p2p.ReSignPeerCertificate(cert.PublicKey, peerID, stageCAPath, stageCAKeyPath)
		if err != nil {
			s.Config.Logger.Error("Failed to re-sign peer certificate", "peerID", peerID, "error", err)
			pushMu.Lock()
			pushFail++
			pushMu.Unlock()
			return err
		}

		err = s.peerClient.RotateTLS(ctx, peerID, protocol.RotateTLSPayload{
			CACert:   string(newCACertPEM),
			NodeCert: string(newCertPEM),
		})
		if err != nil {
			s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", peerID, "error", err)
			pushMu.Lock()
			pushFail++
			pushMu.Unlock()
			return err
		}
		s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", peerID)
		return nil
	})

	if pushFail > 0 {
		s.Config.Logger.Error("CA rotation push incomplete; rolling back in-memory TLS and preserving on-disk CA",
			"failedPeers", pushFail)
		s.tlsMutex.Lock()
		if oldServerMaterial != nil {
			s.serverMaterial.Store(oldServerMaterial)
		}
		if oldClientMaterial != nil {
			s.clientMaterial.Store(oldClientMaterial)
		}
		s.tlsMutex.Unlock()
		return
	}

	// 4. Commit staged material to disk only after every registered peer acknowledged the new material.
	certsDir := filepath.Dir(s.Config.CAPath)
	ownCertFile, ownKeyFile := p2p.NodeCertPaths(certsDir, s.Config.ID)

	stageCAKeyPEM, err := os.ReadFile(stageCAKeyPath)
	if err != nil {
		s.Config.Logger.Error("Failed to read staged CA key", "error", err)
		return
	}
	stageOwnCertPEM, err := os.ReadFile(stageOwnCertFile)
	if err != nil {
		s.Config.Logger.Error("Failed to read staged own cert", "error", err)
		return
	}
	stageOwnKeyPEM, err := os.ReadFile(stageOwnKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to read staged own key", "error", err)
		return
	}

	oldCAKeyPEM, readKeyErr := os.ReadFile(caKeyPath)
	if err := utils.WriteFileAtomic(caKeyPath, stageCAKeyPEM, 0600); err != nil {
		s.Config.Logger.Error("Failed to persist rotated CA key", "error", err)
		s.tlsMutex.Lock()
		if oldServerMaterial != nil {
			s.serverMaterial.Store(oldServerMaterial)
		}
		if oldClientMaterial != nil {
			s.clientMaterial.Store(oldClientMaterial)
		}
		s.tlsMutex.Unlock()
		return
	}
	if err := p2p.WriteNodePEMs(s.Config.CAPath, ownCertFile, ownKeyFile, newCACertPEM, stageOwnCertPEM, stageOwnKeyPEM); err != nil {
		s.Config.Logger.Error("Failed to persist rotated node certs", "error", err)
		if readKeyErr == nil {
			_ = utils.WriteFileAtomic(caKeyPath, oldCAKeyPEM, 0600)
		}
		s.tlsMutex.Lock()
		if oldServerMaterial != nil {
			s.serverMaterial.Store(oldServerMaterial)
		}
		if oldClientMaterial != nil {
			s.clientMaterial.Store(oldClientMaterial)
		}
		s.tlsMutex.Unlock()
		return
	}

	// 5. Reload local TLS now that disk has been updated
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}
