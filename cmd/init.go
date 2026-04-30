package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

var (
	initID      string
	initPort    string
	initStorage string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initializes a new node and cluster from scratch",
	Long:  `Creates the directory structure, generates the Certificate Authority (CA) for the cluster, issues local certificates, and saves the node's configuration.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("🏗️ Initializing node '%s'...\n", initID)
		if err := os.MkdirAll(initStorage, 0755); err != nil {
			fmt.Printf("❌ Error creating storage directory: %v\n", err)
			os.Exit(1)
		}
		certsDir := filepath.Join(initStorage, "certs")
		_ = os.MkdirAll(certsDir, 0755)
		fmt.Println("🔐 Generating cryptographic material...")
		if err := p2p.InitCluster(certsDir); err != nil {
			fmt.Printf("❌ Error generating CA: %v\n", err)
			os.Exit(1)
		}
		if err := p2p.IssueNodeCertificate(certsDir, certsDir, initID); err != nil {
			fmt.Printf("❌ Error generating node certificates: %v\n", err)
			os.Exit(1)
		}
		address := fmt.Sprintf("https://%s:%s", initID, initPort)
		caPath := filepath.Join(certsDir, "ca.crt")

		cfg := protocol.NodeConfig{
			ID:          initID,
			Address:     address,
			StoragePath: initStorage,
			Workers:     4,
			CAPath:      caPath,
		}

		if err := protocol.SaveConfig(cfg); err != nil {
			fmt.Printf("❌ Error saving configuration: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("✅ Initialization completed successfully.")
		fmt.Printf("📂 Environment saved in: %s\n", initStorage)
		fmt.Println("\nYou can now start your node by running:")
		fmt.Println("  proxyma run")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	initCmd.Flags().StringVar(&initID, "id", "", "Node name in the cluster (required)")
	initCmd.Flags().StringVar(&initPort, "port", "8080", "Listening port for IPv4")
	initCmd.Flags().StringVar(&initStorage, "storage", defaultStorage, "Path to the node's anchor directory")

	_ = initCmd.MarkFlagRequired("id")
}
