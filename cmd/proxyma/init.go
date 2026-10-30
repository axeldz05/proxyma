package main

import (
	"fmt"
	"strconv"
	"strings"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

var (
	initID   string
	initPort string
)

func runInit(storagePath, id, port string) error {
	if port == "" {
		port = protocol.DefaultTCPPort
	} else {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return fmt.Errorf("invalid port %q: must be a valid port number", port)
		}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = generateDefaultNodeID()
	}
	if storagePath == "" {
		storagePath = getDefaultStorage()
	}

	fmt.Printf("🏗️ Initializing node '%s'...\n", id)
	address := protocol.HTTPSAddr(id, port)
	fmt.Println("🔐 Generating cryptographic material...")
	if err := p2p.SetupNewNode(storagePath, id, address); err != nil {
		return fmt.Errorf("error initializing node: %w", err)
	}

	fmt.Println("✅ Initialization completed successfully.")
	fmt.Printf("📂 Environment saved in: %s\n", storagePath)
	fmt.Println("\nYou can now start your node by running:")
	fmt.Println("  proxyma run")
	return nil
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initializes a new node and cluster from scratch",
	Long:  `Creates the directory structure, generates the Certificate Authority (CA) for the cluster, issues local certificates, and saves the node's configuration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInit(cliStorage, initID, initPort)
	},
}

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().StringVar(&initID, "id", "", "Node name in the cluster (optional, auto-generated if empty)")
	initCmd.Flags().StringVar(&initPort, "port", protocol.DefaultTCPPort, "Listening port for IPv4")
}
