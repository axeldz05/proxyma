package cmd

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
		cfg, err := protocol.LoadConfig(joinStorage)
		if err != nil {
			fmt.Println("❌ Error: couldn't find config.json. Did you run 'proxyma run' first?")
			os.Exit(1)
		}
		fmt.Printf("🚀 Initializing pairing process for node '%s'...\n", joinID)

		payload, secret, err := p2p.ParseSmartToken(joinToken)
		if err != nil {
			fmt.Printf("❌ Error: Invalid token or corrupt: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("📡 Connecting to the cluster in %s...\n", payload.Address)

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
		client := &http.Client{Transport: tr}

		reqBody := protocol.JoinRequest{
			Secret:  secret,
			CSR:     string(csrPEM),
			ID:		 cfg.ID,
			Address: cfg.Address,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		url := fmt.Sprintf("%s/cluster/join", payload.Address)
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("❌ Network error while connecting to the cluster: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("❌ The cluster rejected the union (Status %d). Is the token expired?\n", resp.StatusCode)
			os.Exit(1)
		}

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
		bootstrapAddr := strings.Replace(payload.Address, "0.0.0.0", "node-1", 1)
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
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}

	joinCmd.Flags().StringVar(&joinToken, "token", "", "El Smart Token provisto por el administrador (requerido)")
	joinCmd.Flags().StringVar(&joinID, "id", "", "El ID único para este nuevo nodo (requerido)")
	joinCmd.Flags().StringVar(&joinStorage, "storage", defaultStorage, "Ruta al directorio de almacenamiento")

	_ = joinCmd.MarkFlagRequired("token")
	_ = joinCmd.MarkFlagRequired("id")
}
