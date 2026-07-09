package main

import (
	"context"
	"fmt"
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

		logFn := func(msg string, err error) {
			if err != nil {
				fmt.Printf("❌ %s: %v\n", msg, err)
			} else {
				fmt.Printf("📡 %s\n", msg)
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		caCert, cert, privKeyPEM, successfulAddr, err := p2p.JoinCluster(ctx, joinToken, joinID, cfg.Address, logFn)
		if err != nil {
			fmt.Printf("❌ Failed to join cluster: %v\n", err)
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

		_ = os.WriteFile(caPath, []byte(caCert), 0644)
		_ = os.WriteFile(certPath, []byte(cert), 0644)
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
