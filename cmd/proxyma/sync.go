package proxyma

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var syncStorage string

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Triggers a full synchronization with all known peers",
	Long:  `Sends a command to the local Proxyma daemon to pull missing files from all nodes registered in its peer list.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrDie(syncStorage)
		sockPath := filepath.Join(cfg.StoragePath, "proxyma.sock")

		fmt.Printf("🔄 Triggering sync via local daemon socket at %s...\n", sockPath)
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			fmt.Printf("❌ Daemon is unreachable. Is 'proxyma run' active? Error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = conn.Close() }()

		_, err = conn.Write([]byte{1})
		if err != nil {
			fmt.Printf("❌ Failed to send sync command: %v\n", err)
			os.Exit(1)
		}

		buf := make([]byte, 1)
		_, err = conn.Read(buf)
		if err != nil {
			fmt.Printf("❌ Connection lost while waiting for sync to finish: %v\n", err)
			os.Exit(1)
		}
		if buf[0] == 0 {
			fmt.Printf("❌ Local daemon failed to complete sync successfully.\n")
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
