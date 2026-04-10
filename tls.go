package main

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
	"os"
	"path/filepath"
	"time"
)

func GenerateOrLoadTLSConfig(caFolderPath, nodeFolderPath, nodeID string) (*tls.Config, *tls.Config, error) {
	caPath := filepath.Join(caFolderPath, "ca.crt")
	caKeyPath := filepath.Join(caFolderPath, "ca.key")
	nodeCertPath := filepath.Join(nodeFolderPath, fmt.Sprintf("%s.crt", nodeID))
	nodeKeyPath := filepath.Join(nodeFolderPath, fmt.Sprintf("%s.key", nodeID))

	var caCert *x509.Certificate
	var caKey *ecdsa.PrivateKey
	var err error

	if fileExists(caPath) && fileExists(caKeyPath) {
		caCert, caKey, err = loadCertAndKey(caPath, caKeyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("error loading CA: %w", err)
		}
	} else {
		caCert, caKey, err = generateCA()
		if err != nil {
			return nil, nil, fmt.Errorf("error generating CA: %w", err)
		}
		saveCertAndKey(caPath, caKeyPath, caCert, caKey)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(caCert)

	var nodeTLSCert tls.Certificate
	if fileExists(nodeCertPath) && fileExists(nodeKeyPath) {
		nodeTLSCert, err = tls.LoadX509KeyPair(nodeCertPath, nodeKeyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("error loading node cert: %w", err)
		}
	} else {
		nodeCert, nodePrivKey, err := generateNodeCert(caCert, caKey, nodeID)
		if err != nil {
			return nil, nil, fmt.Errorf("error generating node cert: %w", err)
		}
		saveCertAndKey(nodeCertPath, nodeKeyPath, nodeCert, nodePrivKey)
		nodeTLSCert, err = tls.LoadX509KeyPair(nodeCertPath, nodeKeyPath)
		if err != nil {
			return nil, nil, err
		}
	}

	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{nodeTLSCert},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	clientTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{nodeTLSCert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS13,
		InsecureSkipVerify: true, 
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			for _, rawCert := range rawCerts {
				cert, err := x509.ParseCertificate(rawCert)
				if err != nil {
					return err
				}
				opts := x509.VerifyOptions{
					Roots: caCertPool,
				}
				if _, err := cert.Verify(opts); err == nil {
					return nil 
				}
			}
			return errors.New("bad certificate: peer certificate not signed by Proxyma CA")
		},
	}

	return serverTLSConfig, clientTLSConfig, nil
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
	defer certOut.Close()
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	privBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})
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
