package p2p

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func InitCluster(caFolderPath string) error {
	caPath := filepath.Join(caFolderPath, "ca.crt")
	caKeyPath := filepath.Join(caFolderPath, "ca.key")

	if fileExists(caPath) && fileExists(caKeyPath) {
		return nil
	}

	caCert, caKey, err := generateCA()
	if err != nil {
		return fmt.Errorf("error generating CA: %w", err)
	}
	return saveCertAndKey(caPath, caKeyPath, caCert, caKey)
}

func IssueNodeCertificate(caFolderPath, nodeFolderPath, nodeID string) error {
	caPath := filepath.Join(caFolderPath, "ca.crt")
	caKeyPath := filepath.Join(caFolderPath, "ca.key")

	caCert, caKey, err := loadCertAndKey(caPath, caKeyPath)
	if err != nil {
		return fmt.Errorf("could not load CA (has the cluster been initialized?): %w", err)
	}

	nodeCertPath := filepath.Join(nodeFolderPath, fmt.Sprintf("%s.crt", nodeID))
	nodeKeyPath := filepath.Join(nodeFolderPath, fmt.Sprintf("%s.key", nodeID))

	nodeCert, nodeKey, err := generateNodeCert(caCert, caKey, nodeID)
	if err != nil {
		return fmt.Errorf("error generating node cert: %w", err)
	}

	return saveCertAndKey(nodeCertPath, nodeKeyPath, nodeCert, nodeKey)
}

func LoadNodeTLS(caCertPath, nodeCertPath, nodeKeyPath string) (*tls.Config, *tls.Config, error) {
	caCertPEM, err := os.ReadFile(caCertPath)
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

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{nodeTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caCertPool,
		MinVersion:   tls.VersionTLS13,
	}

	clientTLS := &tls.Config{
		Certificates: []tls.Certificate{nodeTLSCert},
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

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Proxyma Cluster CA"},
		},
		NotBefore:             time.Now(),
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
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, _ := rand.Int(rand.Reader, serialNumberLimit)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Proxyma Node"},
			CommonName:   nodeID,
		},
		DNSNames:    []string{nodeID, "localhost"}, 
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(1, 0, 0),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	return cert, priv, nil
}

func saveCertAndKey(certPath, keyPath string, cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	certOut, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer func() { _ = certOut.Close() }()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		return fmt.Errorf("failed to encode certificate: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return err
	}

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return err
	}
	defer func() { _ = keyOut.Close() }()
	privBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to encode certificate: %w", err)
	}
	if err := keyOut.Close(); err != nil {
		return err
	}
	return nil
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

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}
