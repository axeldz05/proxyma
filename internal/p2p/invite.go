package p2p

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

type InvitePayload struct {
	Address string `json:"address"`
	CAHash  string `json:"ca_hash"`
}

func GenerateSmartToken(hostAddress string, caCertPath string) (smartToken string, secret string, err error) {
	caBytes, err := os.ReadFile(caCertPath)
	if err != nil {
		return "", "", fmt.Errorf("could not read CA cert: %w", err)
	}
	block, _ := pem.Decode(caBytes)
	if block == nil {
		return "", "", fmt.Errorf("failed to decode CA PEM")
	}
	hash := sha256.Sum256(block.Bytes) 
	caHashHex := hex.EncodeToString(hash[:])

	payload := InvitePayload{
		Address: hostAddress,
		CAHash:  caHashHex,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("could not marshal payload: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random secret: %w", err)
	}
	secretHex := hex.EncodeToString(secretBytes)
	smartToken = fmt.Sprintf("%s.%s", encodedPayload, secretHex)

	return smartToken, secretHex, nil
}

func ParseSmartToken(smartToken string) (payload InvitePayload, secret string, err error) {
	parts := strings.Split(smartToken, ".")
	if len(parts) != 2 {
		return InvitePayload{}, "", fmt.Errorf("invalid token format: must contain exactly one dot")
	}

	encodedPayload, secretHex := parts[0], parts[1]
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return InvitePayload{}, "", fmt.Errorf("invalid base64 payload: %w", err)
	}

	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return InvitePayload{}, "", fmt.Errorf("invalid json payload: %w", err)
	}

	if payload.Address == "" || payload.CAHash == "" {
		return InvitePayload{}, "", fmt.Errorf("missing required fields in token payload")
	}
	if len(secretHex) != 64 {
		return InvitePayload{}, "", fmt.Errorf("invalid secret length")
	}

	return payload, secretHex, nil
}
