package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"proxyma/internal/protocol"
	"strings"
	"time"
)

// JoinCluster performs the network pairing process to join a cluster.
// It parses the Smart Token, generates a CSR/private key, attempts direct connection to all addresses,
// and falls back to a Relay-assisted join if direct connections fail.
// It returns the CA cert, signed cert, private key PEM, and the successful bootstrap address.
func JoinCluster(ctx context.Context, token string, nodeID string, localAddr string, logFn func(string, error)) (caCert string, cert string, privKey []byte, bootstrapNode string, err error) {
	if logFn == nil {
		logFn = func(string, error) {}
	}

	logFn("Parsing Smart Token...", nil)
	payload, secret, err := ParseSmartToken(token)
	if err != nil {
		logFn("Failed to parse Smart Token", err)
		return "", "", nil, "", fmt.Errorf("failed parsing Smart Token: %w", err)
	}
	logFn(fmt.Sprintf("Smart Token parsed successfully. CA Hash: %s, Number of addresses: %d", payload.CAHash[:12], len(payload.Addresses)), nil)

	logFn(fmt.Sprintf("Generating Node CSR for NodeID %q...", nodeID), nil)
	csrPEM, privKeyPEM, err := GenerateNodeCSR(nodeID)
	if err != nil {
		logFn("Failed to generate Node CSR", err)
		return "", "", nil, "", fmt.Errorf("failed to generate Node CSR: %w", err)
	}
	logFn("Node CSR generated successfully.", nil)

	tr := &http.Transport{
		TLSClientConfig: TLSConfigTrustCAHash(payload.CAHash),
	}
	client := NewHTTPClient(tr, 3*time.Second)

	reqBody := protocol.JoinRequest{
		Secret:  secret,
		CSR:     string(csrPEM),
		ID:      nodeID,
		Address: localAddr,
	}

	var resp *http.Response
	var errs []string
	var successfulAddr string

	logFn("Starting cluster join loops over packed addresses...", nil)
	for _, addr := range payload.Addresses {
		urlStr := fmt.Sprintf("%s%s", addr, protocol.PathClusterJoin)
		logFn(fmt.Sprintf("Attempting connection to address: %s", urlStr), nil)
		r, doErr := PostJSONAbsolute(ctx, client, urlStr, reqBody)
		if doErr != nil {
			logFn(fmt.Sprintf("Connection/TLS error to %s", addr), doErr)
			errs = append(errs, fmt.Sprintf("- [%s]: Connection/TLS error: %v", addr, doErr))
			continue
		}
		if r.StatusCode != http.StatusOK {
			_ = r.Body.Close()
			logFn(fmt.Sprintf("Cluster rejected join for %s with status %d", addr, r.StatusCode), nil)
			errs = append(errs, fmt.Sprintf("- [%s]: Cluster rejected join: Status %d", addr, r.StatusCode))
			continue
		}
		resp = r
		successfulAddr = addr
		logFn(fmt.Sprintf("Connection successful to %s", addr), nil)
		break
	}

	// Try Relay Fallback if direct connections failed
	if resp == nil && payload.RelayAddr != "" && payload.SponsorID != "" {
		logFn(fmt.Sprintf("Direct connections failed. Attempting Relay-assisted join via %s to target sponsor %s...", payload.RelayAddr, payload.SponsorID), nil)

		bodyBytes, _ := json.Marshal(reqBody)
		relayReq := NewRelayRequest(payload.SponsorID, http.MethodPost, protocol.PathClusterJoin, bodyBytes, map[string]string{
			"Content-Type": "application/json",
		})
		logFn(fmt.Sprintf("Sending relayed join request to: %s%s", payload.RelayAddr, protocol.PathRelayForward), nil)
		relayRes, fwdErr := ForwardRelay(ctx, client.Transport, payload.RelayAddr, relayReq)
		if fwdErr == nil {
			if relayRes.StatusCode == http.StatusOK {
				resp = relayRes.ToHTTPResponse(nil)
				successfulAddr = payload.RelayAddr
				logFn("Relay-assisted join succeeded!", nil)
			} else {
				logFn(fmt.Sprintf("Relayed join rejected by sponsor: Status %d, Error: %s", relayRes.StatusCode, string(relayRes.Body)), nil)
				errs = append(errs, fmt.Sprintf("- [Relay %s]: Sponsor rejected join: Status %d", payload.RelayAddr, relayRes.StatusCode))
			}
		} else {
			logFn("Relay connection/TLS error", fwdErr)
			errs = append(errs, fmt.Sprintf("- [Relay %s]: Connection error: %v", payload.RelayAddr, fwdErr))
		}
	}

	if resp == nil {
		detailedErrorMsg := fmt.Sprintf(
			"Failed to join cluster. All attempted addresses failed:\n%s\n\nSecret Prefix: %s...\nCA Hash: %s\n\n💡 Tip: If devices are on the same Wi-Fi network:\n1. Ensure the hosting PC's firewall allows incoming traffic on port 8080.\n2. Ensure AP/Client Isolation is disabled on the router.",
			strings.Join(errs, "\n"),
			secret[:8],
			payload.CAHash[:12],
		)
		errJoin := errors.New(detailedErrorMsg)
		logFn("All join attempts failed", errJoin)
		return "", "", nil, "", errJoin
	}
	defer func() { _ = resp.Body.Close() }()

	logFn("Decoding cluster JoinResponse...", nil)
	var joinResp protocol.JoinResponse
	if errDec := json.NewDecoder(resp.Body).Decode(&joinResp); errDec != nil {
		logFn("Failed decoding cluster response", errDec)
		return "", "", nil, "", fmt.Errorf("failed decoding cluster response from %s: %w", successfulAddr, errDec)
	}
	logFn("JoinResponse decoded successfully.", nil)

	return joinResp.CACert, joinResp.Certificate, privKeyPEM, successfulAddr, nil
}
