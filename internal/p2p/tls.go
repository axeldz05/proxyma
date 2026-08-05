package p2p

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

const leafOrgName = "Proxyma Cluster"

func InitCluster(caFolderPath string) error {
	caPath, caKeyPath := CACertPaths(caFolderPath)

	if utils.FileExists(caPath) && utils.FileExists(caKeyPath) {
		return nil
	}

	caCert, caKey, err := generateCA()
	if err != nil {
		return fmt.Errorf("error generating CA: %w", err)
	}
	return saveCertAndKey(caPath, caKeyPath, caCert, caKey)
}

func IssueNodeCertificate(caFolderPath, nodeFolderPath, nodeID string) error {
	caPath, caKeyPath := CACertPaths(caFolderPath)

	caCert, caKey, err := loadCertAndKey(caPath, caKeyPath)
	if err != nil {
		return fmt.Errorf("could not load CA (has the cluster been initialized?): %w", err)
	}

	nodeCertPath, nodeKeyPath := NodeCertPaths(nodeFolderPath, nodeID)

	nodeCert, nodeKey, err := generateNodeCert(caCert, caKey, nodeID)
	if err != nil {
		return fmt.Errorf("error generating node cert: %w", err)
	}

	return saveCertAndKey(nodeCertPath, nodeKeyPath, nodeCert, nodeKey)
}

func LoadNodeTLS(caCertPath, nodeCertPath, nodeKeyPath string) (*tls.Config, *tls.Config, error) {
	caCertPEM, err := ReadCAPEM(caCertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading CA cert: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertPEM) {
		return nil, nil, errors.New("failed to append CA cert to pool")
	}
	nodeTLSCert, err := tls.LoadX509KeyPair(nodeCertPath, nodeKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading node key pair: %w", err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock != nil {
		nodeTLSCert.Certificate = append(nodeTLSCert.Certificate, caBlock.Bytes)
	}

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{nodeTLSCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    caCertPool,
		MinVersion:   tls.VersionTLS13,
	}

	clientTLS := &tls.Config{
		Certificates:       []tls.Certificate{nodeTLSCert},
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no certificates provided by peer")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("failed to parse certificate: %w", err)
			}
			opts := x509.VerifyOptions{
				Roots:       caCertPool,
				CurrentTime: time.Now(),
			}
			_, err = cert.Verify(opts)
			if err != nil {
				return fmt.Errorf("bad certificate: %w", err)
			}
			return nil
		},
		MinVersion: tls.VersionTLS13,
	}

	return serverTLS, clientTLS, nil
}

func GenerateNodeCSR(nodeID string) (csrPEM []byte, privateKeyPEM []byte, err error) {
	privateKey, err := generatePrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	privBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	privateKeyPEM = encodeECKeyPEM(privBytes)

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   nodeID,
			Organization: []string{leafOrgName},
		},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	csrPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrBytes,
	})

	return csrPEM, privateKeyPEM, nil
}

// newNodeCertTemplate builds a leaf node certificate template (SSOT for KeyUsage/SANs).
func newNodeCertTemplate(subject pkix.Name, dnsNames []string) x509.Certificate {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, _ := rand.Int(rand.Reader, serialNumberLimit)
	return x509.Certificate{
		SerialNumber:          serialNumber,
		DNSNames:              dnsNames,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		Subject:               subject,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
}

func SignCSR(csrPEM []byte, caCertPath string, caKeyPath string) (certPEM []byte, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("failed to decode PEM block containing CSR")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSR: %w", err)
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature: %w", err)
	}

	caCert, caPrivKey, err := loadCertAndKey(caCertPath, caKeyPath)
	if err != nil {
		return nil, err
	}

	return signLeaf(csr.PublicKey, csr.Subject, LeafDNSNames(csr.Subject.CommonName), caCert, caPrivKey)
}

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	priv, err := generatePrivateKey()
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Proxyma Cluster CA"},
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	return cert, priv, nil
}

func generateNodeCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	priv, err := generatePrivateKey()
	if err != nil {
		return nil, nil, err
	}

	pemBytes, err := signLeaf(&priv.PublicKey, pkix.Name{
		Organization: []string{leafOrgName},
		CommonName:   nodeID,
	}, LeafDNSNames(nodeID), caCert, caKey)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode signed leaf PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, priv, nil
}

func saveCertAndKey(certPath, keyPath string, cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	privBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, encodeCertPEM(cert.Raw), 0644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, encodeECKeyPEM(privBytes), 0600)
}

// LeafDNSNames returns the DNS SANs for a node leaf certificate (L1).
func LeafDNSNames(nodeID string) []string {
	return []string{nodeID, "localhost"}
}

func loadCertAndKey(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ = pem.Decode(keyPEM)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func generatePrivateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// SetupNewNode initializes the directories, CA, node certificate, and saves the initial config.
func SetupNewNode(storagePath, nodeID, address string) error {
	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return fmt.Errorf("error creating storage directory: %w", err)
	}
	certsDir := filepath.Join(storagePath, "certs")
	_ = os.MkdirAll(certsDir, 0755)

	if err := InitCluster(certsDir); err != nil {
		return fmt.Errorf("error generating CA: %w", err)
	}
	if err := IssueNodeCertificate(certsDir, certsDir, nodeID); err != nil {
		return fmt.Errorf("error generating node certificates: %w", err)
	}

	caPath, _ := CACertPaths(certsDir)
	cfg := protocol.NodeConfig{
		ID:          nodeID,
		Address:     address,
		StoragePath: storagePath,
		Workers:     4,
		CAPath:      caPath,
	}

	return protocol.SaveConfig(cfg)
}

func RotateCA(caFolderPath string) error {
	caPath, caKeyPath := CACertPaths(caFolderPath)

	caCert, caKey, err := generateCA()
	if err != nil {
		return fmt.Errorf("error generating new CA: %w", err)
	}
	return saveCertAndKey(caPath, caKeyPath, caCert, caKey)
}

func ReSignPeerCertificate(peerPubKey any, peerID string, caCertPath, caKeyPath string) (certPEM []byte, err error) {
	caCert, caPrivKey, err := loadCertAndKey(caCertPath, caKeyPath)
	if err != nil {
		return nil, err
	}

	return signLeaf(peerPubKey, pkix.Name{
		CommonName:   peerID,
		Organization: []string{leafOrgName},
	}, LeafDNSNames(peerID), caCert, caPrivKey)
}

func signLeaf(pub any, subject pkix.Name, dnsNames []string, caCert *x509.Certificate, caPrivKey any) ([]byte, error) {
	certTemplate := newNodeCertTemplate(subject, dnsNames)
	certBytes, err := x509.CreateCertificate(rand.Reader, &certTemplate, caCert, pub, caPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign certificate: %w", err)
	}
	return encodeCertPEM(certBytes), nil
}

func encodeCertPEM(certDER []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

func encodeECKeyPEM(keyDER []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// CAKeyPath derives the CA private key path from the CA certificate path.
func CAKeyPath(caCertPath string) string {
	return strings.Replace(caCertPath, ".crt", ".key", 1)
}

// CACertPaths returns ca.crt and ca.key under dir.
func CACertPaths(dir string) (certPath, keyPath string) {
	certPath = filepath.Join(dir, "ca.crt")
	return certPath, CAKeyPath(certPath)
}

// NodeCertPaths returns the node certificate and key paths under dir.
func NodeCertPaths(dir, nodeID string) (certPath, keyPath string) {
	return filepath.Join(dir, fmt.Sprintf("%s.crt", nodeID)), filepath.Join(dir, fmt.Sprintf("%s.key", nodeID))
}

// ReadCAPEM reads CA certificate PEM bytes from disk (L1).
func ReadCAPEM(caPath string) ([]byte, error) {
	return os.ReadFile(caPath)
}

// ResolveNodeCertPaths returns node cert/key paths next to the CA, with storagePath fallback for test layouts (L2).
func ResolveNodeCertPaths(caPath, storagePath, nodeID string) (certPath, keyPath string) {
	certPath, keyPath = NodeCertPaths(filepath.Dir(caPath), nodeID)
	if _, err := os.Stat(keyPath); os.IsNotExist(err) && storagePath != "" {
		altCert, altKey := NodeCertPaths(storagePath, nodeID)
		if _, err := os.Stat(altKey); err == nil {
			return altCert, altKey
		}
	}
	return certPath, keyPath
}

// PEMCertDER extracts certificate DER bytes from PEM (L1).
func PEMCertDER(pemData []byte) ([]byte, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode CA PEM")
	}
	return block.Bytes, nil
}

// HashCertDER returns the hex SHA-256 of a certificate DER (L1).
func HashCertDER(der []byte) string {
	hash := sha256.Sum256(der)
	return hex.EncodeToString(hash[:])
}

// CAHashFromPEM decodes a PEM certificate and returns HashCertDER of its DER bytes (L1).
func CAHashFromPEM(pemData []byte) (string, error) {
	der, err := PEMCertDER(pemData)
	if err != nil {
		return "", err
	}
	return HashCertDER(der), nil
}

// TLSConfigTrustCAHash builds a TLS client config that accepts peers whose leaf DER hashes to caHash (L1).
func TLSConfigTrustCAHash(caHash string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			for _, rawCert := range rawCerts {
				if HashCertDER(rawCert) == caHash {
					return nil
				}
			}
			return errors.New("security alert: identity mismatch")
		},
	}
}

// WriteNodePEMs writes CA/node cert/key PEM bytes to disk (L2).
// keyPEM may be empty (e.g. CA rotation that only updates certs).
func WriteNodePEMs(caPath, certPath, keyPath string, caPEM, certPEM, keyPEM []byte) error {
	if err := os.WriteFile(caPath, caPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return err
	}
	if len(keyPEM) > 0 && keyPath != "" {
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			return err
		}
	}
	return nil
}

// PeerCNFromTLS extracts the peer CommonName from a TLS connection state.
func PeerCNFromTLS(state *tls.ConnectionState) (string, bool) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", false
	}
	cn := state.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", false
	}
	return cn, true
}
