package p2p

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strconv"
	"strings"
)

type InvitePayload struct {
	Address   string   `json:"address"`
	CAHash    string   `json:"ca_hash"`
	Addresses []string `json:"addresses,omitempty"`
	SponsorID string   `json:"sponsor_id,omitempty"`
	RelayAddr string   `json:"relay_addr,omitempty"`
}

func GenerateSmartToken(hostAddress string, caCertPath string, sponsorID string, relayAddr string) (smartToken string, secret string, err error) {
	caBytes, err := ReadCAPEM(caCertPath)
	if err != nil {
		return "", "", fmt.Errorf("could not read CA cert: %w", err)
	}
	hashHex, err := CAHashFromPEM(caBytes)
	if err != nil {
		return "", "", err
	}
	hash, err := hex.DecodeString(hashHex)
	if err != nil || len(hash) != 32 {
		return "", "", fmt.Errorf("invalid CA hash from PEM")
	}

	// Generate a 32-byte random secret
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random secret: %w", err)
	}
	secretHex := hex.EncodeToString(secretBytes)

	// Parse hostAddress to extract the port
	cleanAddr := utils.StripURLScheme(hostAddress)

	host, portStr, pErr := net.SplitHostPort(cleanAddr)
	var port uint16
	_, _ = fmt.Sscanf(protocol.DefaultTCPPort, "%d", &port)
	if pErr == nil {
		if p, pErr2 := strconv.Atoi(portStr); pErr2 == nil {
			port = uint16(p)
		}
	} else {
		host = cleanAddr
	}

	// Gather all local IPs
	var ips []net.IP
	// 1. Prioritize host IP if it parses as an IP
	if parsedHost := net.ParseIP(host); parsedHost != nil {
		ips = append(ips, parsedHost)
	}

	// 2. Discover local interface IPs
	localIPs, _ := utils.GetRoutableLocalIPs()
	for _, lip := range localIPs {
		dup := false
		for _, ip := range ips {
			if ip.Equal(lip) {
				dup = true
				break
			}
		}
		if !dup {
			ips = append(ips, lip)
		}
	}

	// If no IPs found at all, fallback to loopback
	if len(ips) == 0 && (host == "" || net.ParseIP(host) == nil) {
		ips = append(ips, net.IPv4(127, 0, 0, 1))
	}

	// Build binary packet
	// Format:
	// 1 byte: Version (0x02)
	// 32 bytes: CA Hash
	// 32 bytes: Secret
	// 2 bytes: Port
	// 1 byte: Num addresses/entries
	// For each entry:
	//   1 byte: Entry Type (1 = IPv4, 2 = IPv6, 3 = Hostname, 4 = Relay Info)
	//   If IPv4: 4 bytes
	//   If IPv6: 16 bytes
	//   If Hostname: 1 byte length + N bytes
	//   If Relay Info: 1 byte SponsorID len + N bytes SponsorID + 1 byte RelayAddr len + M bytes RelayAddr
	var buf bytes.Buffer
	buf.WriteByte(2) // Version 2
	buf.Write(hash)
	buf.Write(secretBytes)

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, port)
	buf.Write(portBytes)

	isHostname := (host != "" && net.ParseIP(host) == nil && host != "localhost")
	hasRelay := (sponsorID != "" && relayAddr != "")
	numEntries := len(ips)
	if isHostname {
		numEntries++
	}
	if hasRelay {
		numEntries++
	}
	buf.WriteByte(byte(numEntries))

	if isHostname {
		buf.WriteByte(3) // Hostname type
		hostBytes := []byte(host)
		if len(hostBytes) > 255 {
			hostBytes = hostBytes[:255]
		}
		buf.WriteByte(byte(len(hostBytes)))
		buf.Write(hostBytes)
	}

	if hasRelay {
		buf.WriteByte(4) // Relay Info type
		sidBytes := []byte(sponsorID)
		if len(sidBytes) > 255 {
			sidBytes = sidBytes[:255]
		}
		buf.WriteByte(byte(len(sidBytes)))
		buf.Write(sidBytes)

		raddrBytes := []byte(relayAddr)
		if len(raddrBytes) > 255 {
			raddrBytes = raddrBytes[:255]
		}
		buf.WriteByte(byte(len(raddrBytes)))
		buf.Write(raddrBytes)
	}

	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			buf.WriteByte(1) // IPv4 type
			buf.Write(ip4)
		} else if ip16 := ip.To16(); ip16 != nil {
			buf.WriteByte(2) // IPv6 type
			buf.Write(ip16)
		}
	}

	smartToken = base64.RawURLEncoding.EncodeToString(buf.Bytes())
	return smartToken, secretHex, nil
}

func ParseSmartToken(smartToken string) (payload InvitePayload, secret string, err error) {
	// Trim any surrounding quotes or spaces
	smartToken = strings.TrimSpace(smartToken)
	smartToken = strings.Trim(smartToken, "\"'")

	if smartToken == "" {
		return InvitePayload{}, "", fmt.Errorf("token is empty")
	}

	// Decode base64
	data, err := base64.RawURLEncoding.DecodeString(smartToken)
	if err == nil && len(data) > 0 && data[0] == 2 {
		// Process Version 2 Binary format
		if len(data) < 1+32+32+2+1 {
			return InvitePayload{}, "", fmt.Errorf("invalid binary token length")
		}
		caHash := data[1:33]
		secretBytes := data[33:65]
		port := binary.BigEndian.Uint16(data[65:67])
		numEntries := int(data[67])

		payload.CAHash = hex.EncodeToString(caHash)
		secret = hex.EncodeToString(secretBytes)

		idx := 68
		for i := 0; i < numEntries; i++ {
			if idx >= len(data) {
				return InvitePayload{}, "", fmt.Errorf("malformed binary token: out of bounds")
			}
			entryType := data[idx]
			idx++
			switch entryType {
			case 1: // IPv4
				if idx+4 > len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: ipv4 out of bounds")
				}
				ip := net.IP(data[idx : idx+4])
				idx += 4
				payload.Addresses = append(payload.Addresses, fmt.Sprintf("https://%s:%d", ip.String(), port))
			case 2: // IPv6
				if idx+16 > len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: ipv6 out of bounds")
				}
				ip := net.IP(data[idx : idx+16])
				idx += 16
				payload.Addresses = append(payload.Addresses, fmt.Sprintf("https://[%s]:%d", ip.String(), port))
			case 3: // Hostname
				if idx >= len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: hostname len out of bounds")
				}
				hostLen := int(data[idx])
				idx++
				if idx+hostLen > len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: hostname out of bounds")
				}
				host := string(data[idx : idx+hostLen])
				idx += hostLen
				payload.Addresses = append(payload.Addresses, fmt.Sprintf("https://%s:%d", host, port))
			case 4: // Relay Information
				if idx >= len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: relay sid len out of bounds")
				}
				sidLen := int(data[idx])
				idx++
				if idx+sidLen > len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: relay sid out of bounds")
				}
				payload.SponsorID = string(data[idx : idx+sidLen])
				idx += sidLen

				if idx >= len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: relay addr len out of bounds")
				}
				raddrLen := int(data[idx])
				idx++
				if idx+raddrLen > len(data) {
					return InvitePayload{}, "", fmt.Errorf("malformed binary token: relay addr out of bounds")
				}
				payload.RelayAddr = string(data[idx : idx+raddrLen])
				idx += raddrLen
			default:
				return InvitePayload{}, "", fmt.Errorf("malformed binary token: unknown entry type %d", entryType)
			}
		}

		if len(payload.Addresses) > 0 {
			payload.Address = payload.Addresses[0]
		}
		return payload, secret, nil
	}

	// Fallback to Version 1 JSON format
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

	// Populate Addresses slice with the single address for V1
	payload.Addresses = []string{payload.Address}

	return payload, secretHex, nil
}
