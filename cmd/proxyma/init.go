package proxyma

import (
	"fmt"
	"os"
	"proxyma/internal/p2p"

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
		if initID == "" {
			initID = generateDefaultNodeID()
		}
		fmt.Printf("🏗️ Initializing node '%s'...\n", initID)
		address := fmt.Sprintf("https://%s:%s", initID, initPort)
		fmt.Println("🔐 Generating cryptographic material...")
		if err := p2p.SetupNewNode(initStorage, initID, address); err != nil {
			fmt.Printf("❌ Error initializing node: %v\n", err)
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

	defaultStorage := getDefaultStorage()
	initCmd.Flags().StringVar(&initID, "id", "", "Node name in the cluster (optional, auto-generated if empty)")
	initCmd.Flags().StringVar(&initPort, "port", "8080", "Listening port for IPv4")
	initCmd.Flags().StringVar(&initStorage, "storage", defaultStorage, "Path to the node's anchor directory")
}
