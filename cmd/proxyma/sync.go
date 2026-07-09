package main

import (
	"fmt"
	"os"

	proxyma_bind "proxyma/cmd/proxyma-bind"

	"github.com/spf13/cobra"
)

var syncStorage string

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Triggers a full synchronization with all known peers",
	Long:  `Sends a command to the local Proxyma daemon to pull missing files from all nodes registered in its peer list.`,
	Run: func(cmd *cobra.Command, args []string) {
		_ = loadConfigOrDie(syncStorage)
		proxyma_bind.SetStoragePath(syncStorage)

		fmt.Println("🔄 Triggering sync via local daemon...")
		errStr := proxyma_bind.SyncVFS()
		if errStr != "" {
			fmt.Printf("❌ Failed to complete sync: %s\n", errStr)
			os.Exit(1)
		}

		fmt.Println("✅ Sync triggered successfully across the cluster.")
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
	defaultStorage := getDefaultStorage()
	syncCmd.Flags().StringVar(&syncStorage, "storage", defaultStorage, "Path to the local node's directory")
}
