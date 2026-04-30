package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

var syncStorage string

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Triggers a full synchronization with all known peers",
	Long:  `Sends a command to the local Proxyma daemon to pull missing files from all nodes registered in its peer list.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := protocol.LoadConfig(syncStorage)
		if err != nil {
			fmt.Println("❌ Error: Couldn't find config.json. Run 'proxyma init' or 'proxyma join' first.")
			os.Exit(1)
		}

		fmt.Printf("🔄 Contacting local daemon at %s to start sync...\n", cfg.Address)
		client := setupLocalAdminClient(cfg)
		url := fmt.Sprintf("%s/sync", cfg.Address)
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			fmt.Printf("❌ Failed to create request: %v\n", err)
			os.Exit(1)
		}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("❌ Daemon is unreachable. Is 'proxyma run' active? Error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			fmt.Println("✅ Sync triggered successfully across the cluster.")
		} else {
			fmt.Printf("❌ Local daemon rejected sync request (Status %d): %s\n", resp.StatusCode, string(respBody))
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	syncCmd.Flags().StringVar(&syncStorage, "storage", defaultStorage, "Path to the local node's directory")
}
