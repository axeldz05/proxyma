package p2p_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"proxyma/internal/p2p"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartTokenV2(t *testing.T) {
	tmpDir := t.TempDir()
	caCertPath, _ := p2p.CACertPaths(tmpDir)

	// Generate a dummy self-signed CA PEM for hashing
	caBytes := []byte("-----BEGIN CERTIFICATE-----\nMIIB8zCCAXqgAwIBAgIJAOc84G...") // truncated PEM structure
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	err := os.WriteFile(caCertPath, pemData, 0644)
	require.NoError(t, err)

	expectedHash := sha256.Sum256(caBytes)
	expectedHashHex := hex.EncodeToString(expectedHash[:])

	// 1. Generate token with an IPv4 address
	hostIPv4 := "192.168.1.100:8080"
	token, secret, err := p2p.GenerateSmartToken(hostIPv4, caCertPath, "my-sponsor-node", "https://relay.proxyma.net:8080")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Len(t, secret, 64)

	// Since it's V2 (compact binary), there should be no dot in it
	require.NotContains(t, token, ".")

	// 2. Parse token
	payload, parsedSecret, err := p2p.ParseSmartToken(token)
	require.NoError(t, err)
	require.Equal(t, secret, parsedSecret)
	require.Equal(t, expectedHashHex, payload.CAHash)
	require.NotEmpty(t, payload.Addresses)
	require.Equal(t, "my-sponsor-node", payload.SponsorID)
	require.Equal(t, "https://relay.proxyma.net:8080", payload.RelayAddr)

	// The first IP should match the host
	require.Contains(t, payload.Addresses[0], "192.168.1.100")
	require.Contains(t, payload.Addresses[0], "8080")

	// 3. Test trimming of quotes and whitespace
	payloadTrimmed, _, err := p2p.ParseSmartToken("  \"" + token + "\"  ")
	require.NoError(t, err)
	require.Equal(t, payload.Addresses, payloadTrimmed.Addresses)
	require.Equal(t, payload.SponsorID, payloadTrimmed.SponsorID)
	require.Equal(t, payload.RelayAddr, payloadTrimmed.RelayAddr)
}

func TestSmartTokenBackwardsCompatibility(t *testing.T) {
	// Create a mock V1 payload (JSON based format)
	v1Payload := struct {
		Address string `json:"address"`
		CAHash  string `json:"ca_hash"`
	}{
		Address: "https://192.168.0.228:8080",
		CAHash:  "d054b85ac11939af0e8735062ef97a832b3e07f0f190d3f44eb361b6f6adfbb87",
	}

	payloadBytes, err := json.Marshal(v1Payload)
	require.NoError(t, err)

	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	secretHex := strings.Repeat("a", 64) // 64 hex characters
	v1Token := encodedPayload + "." + secretHex

	// Parse it as a legacy token
	payload, secret, err := p2p.ParseSmartToken(v1Token)
	require.NoError(t, err)
	require.Equal(t, secretHex, secret)
	require.Equal(t, v1Payload.Address, payload.Address)
	require.Equal(t, v1Payload.CAHash, payload.CAHash)
	require.Len(t, payload.Addresses, 1)
	require.Equal(t, v1Payload.Address, payload.Addresses[0])
}

func TestParseHostAddressHandling(t *testing.T) {
	tmpDir := t.TempDir()
	caCertPath, _ := p2p.CACertPaths(tmpDir)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("ca"),
	})
	require.NoError(t, os.WriteFile(caCertPath, pemData, 0644))

	// Hostname instead of IP
	token, _, err := p2p.GenerateSmartToken("https://my-awesome-cluster.net:9090", caCertPath, "", "")
	require.NoError(t, err)

	payload, _, err := p2p.ParseSmartToken(token)
	require.NoError(t, err)
	require.NotEmpty(t, payload.Addresses)
	require.Contains(t, payload.Addresses[0], "my-awesome-cluster.net:9090")
}
