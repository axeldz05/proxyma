package proxyma

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	joinToken   string
	joinID      string
	joinStorage string
)

var joinCmd = &cobra.Command{
	Use:   "join",
	Short: "Use an Invite Token to join another cluster",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrDie(joinStorage)
		if joinID == "" {
			joinID = generateDefaultNodeID()
		}
		fmt.Printf("🚀 Initializing pairing process for node '%s'...\n", joinID)

		payload, secret, err := p2p.ParseSmartToken(joinToken)
		if err != nil {
			fmt.Printf("❌ Error: Invalid token or corrupt: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("📡 Connecting to the cluster...\n")

		csrPEM, privKeyPEM, err := p2p.GenerateNodeCSR(joinID)
		if err != nil {
			fmt.Printf("❌ Error generating CSR: %v\n", err)
			os.Exit(1)
		}

		tr := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
					for _, rawCert := range rawCerts {
						hash := sha256.Sum256(rawCert)
						if hex.EncodeToString(hash[:]) == payload.CAHash {
							return nil
						}
					}
					return fmt.Errorf("security alert: the identity of the server does not match with the invitation code")
				},
			},
		}
		client := &http.Client{
			Transport: tr,
			Timeout:   3 * time.Second,
		}

		reqBody := protocol.JoinRequest{
			Secret:  secret,
			CSR:     string(csrPEM),
			ID:      cfg.ID,
			Address: cfg.Address,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		var resp *http.Response
		var lastErr error
		var successfulAddr string

		for _, addr := range payload.Addresses {
			url := fmt.Sprintf("%s/cluster/join", addr)
			req, reqErr := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(bodyBytes))
			if reqErr != nil {
				lastErr = reqErr
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			r, doErr := client.Do(req)
			if doErr != nil {
				lastErr = doErr
				continue
			}
			if r.StatusCode != http.StatusOK {
				_ = r.Body.Close()
				lastErr = fmt.Errorf("status %d", r.StatusCode)
				continue
			}
			resp = r
			successfulAddr = addr
			break
		}

		if resp == nil {
			fmt.Printf("❌ There was an error trying to connect to the cluster: %v\n", lastErr)
			fmt.Println("\n💡 Tip: If devices are on the same Wi-Fi network:")
			fmt.Println("1. Ensure the hosting PC's firewall allows incoming traffic on port 8080.")
			fmt.Println("2. Ensure AP/Client Isolation is disabled on the router.")
			os.Exit(1)
		}
		defer func() { _ = resp.Body.Close() }()

		var joinResp protocol.JoinResponse
		if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
			fmt.Println("❌ Error decoding the cluster response.")
			os.Exit(1)
		}

		certsDir := filepath.Join(joinStorage, "certs")
		if err := os.MkdirAll(certsDir, 0755); err != nil {
			fmt.Printf("❌ Error creating the certificate directory: %v\n", err)
			os.Exit(1)
		}

		caPath := filepath.Join(certsDir, "ca.crt")
		certPath := filepath.Join(certsDir, fmt.Sprintf("%s.crt", joinID))
		keyPath := filepath.Join(certsDir, fmt.Sprintf("%s.key", joinID))

		_ = os.WriteFile(caPath, []byte(joinResp.CACert), 0644)
		_ = os.WriteFile(certPath, []byte(joinResp.Certificate), 0644)
		_ = os.WriteFile(keyPath, privKeyPEM, 0600)

		newCfg := cfg
		newCfg.ID = joinID
		newCfg.StoragePath = joinStorage
		newCfg.CAPath = caPath
		bootstrapAddr := strings.Replace(successfulAddr, "0.0.0.0", "node-1", 1)
		newCfg.BootstrapNode = bootstrapAddr
		err = protocol.SaveConfig(newCfg)
		if err != nil {
			fmt.Printf("❌ Error saving new config for joining: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("✅ Successful cluster joining!")
		fmt.Printf("Your certificates have been saved in: %s\n", certsDir)
		fmt.Println("\nYou can now start your node by running:")
		fmt.Println("  proxyma run")
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
	defaultStorage := getDefaultStorage()

	joinCmd.Flags().StringVar(&joinToken, "token", "", "The Smart Token provided by the administrator (required)")
	joinCmd.Flags().StringVar(&joinID, "id", "", "The unique ID for this new node (optional, auto-generated if empty)")
	joinCmd.Flags().StringVar(&joinStorage, "storage", defaultStorage, "Path to the storage directory")

	_ = joinCmd.MarkFlagRequired("token")
}
