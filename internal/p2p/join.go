package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"proxyma/internal/protocol"
	"strings"
)

func abbreviated(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func validateJoinEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid join endpoint %q", raw)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("join endpoint must use HTTPS: %q", raw)
	}
	return nil
}

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
	logFn(fmt.Sprintf("Smart Token parsed successfully. CA Hash: %s, Number of addresses: %d", abbreviated(payload.CAHash, 12), len(payload.Addresses)), nil)
	if err := ctx.Err(); err != nil {
		return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", err)
	}

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
	client := NewHTTPClient(tr, protocol.DialTimeoutJoin)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		// Join requests carry a one-time invite secret. Never replay it to a
		// redirect target, even when the target is also HTTPS.
		return http.ErrUseLastResponse
	}

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
	safeDirect, staleIPs := splitDirectAddresses(payload.Addresses)
	attemptDirect := func(addresses []string) error {
		for _, addr := range addresses {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if endpointErr := validateJoinEndpoint(addr); endpointErr != nil {
				logFn("Skipping unsafe cluster join address", endpointErr)
				errs = append(errs, fmt.Sprintf("- [%s]: %v", addr, endpointErr))
				continue
			}
			urlStr := fmt.Sprintf("%s%s", addr, protocol.PathClusterJoin)
			logFn(fmt.Sprintf("Attempting connection to address: %s", urlStr), nil)
			r, doErr := PostJSONAbsolute(ctx, client, urlStr, reqBody)
			if doErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
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
			return nil
		}
		return nil
	}

	// Match the regular router policy: stable DNS names first, relay before
	// stale bridge/LAN/STUN IP literals.
	if attemptErr := attemptDirect(safeDirect); attemptErr != nil {
		return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", attemptErr)
	}

	relayUsable := payload.RelayAddr != "" && payload.SponsorID != ""
	if relayUsable {
		if endpointErr := validateJoinEndpoint(payload.RelayAddr); endpointErr != nil {
			logFn("Skipping unsafe relay join address", endpointErr)
			errs = append(errs, fmt.Sprintf("- [Relay %s]: %v", payload.RelayAddr, endpointErr))
			relayUsable = false
		}
	}

	// Try Relay Fallback if direct connections failed.
	if resp == nil && relayUsable {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", ctxErr)
		}
		logFn(fmt.Sprintf("Direct connections failed. Attempting Relay-assisted join via %s to target sponsor %s...", payload.RelayAddr, payload.SponsorID), nil)

		bodyBytes, marshalErr := json.Marshal(reqBody)
		if marshalErr != nil {
			return "", "", nil, "", fmt.Errorf("encode relayed join request: %w", marshalErr)
		}
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
				logFn(fmt.Sprintf("Relayed join rejected by sponsor: Status %d", relayRes.StatusCode), nil)
				errs = append(errs, fmt.Sprintf("- [Relay %s]: Sponsor rejected join: Status %d", payload.RelayAddr, relayRes.StatusCode))
			}
		} else {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", ctxErr)
			}
			logFn("Relay connection/TLS error", fwdErr)
			errs = append(errs, fmt.Sprintf("- [Relay %s]: Connection error: %v", payload.RelayAddr, fwdErr))
		}
	}

	if resp == nil {
		if attemptErr := attemptDirect(staleIPs); attemptErr != nil {
			return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", attemptErr)
		}
	}

	if resp == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", nil, "", fmt.Errorf("cluster join canceled: %w", ctxErr)
		}
		detailedErrorMsg := fmt.Sprintf(
			"Failed to join cluster. All attempted addresses failed:\n%s\n\nCA Hash: %s\n\n💡 Tip: If devices are on the same Wi-Fi network:\n1. Ensure the hosting PC's firewall allows incoming traffic on port 8080.\n2. Ensure AP/Client Isolation is disabled on the router.",
			strings.Join(errs, "\n"),
			abbreviated(payload.CAHash, 12),
		)
		errJoin := fmt.Errorf("%s", detailedErrorMsg)
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
