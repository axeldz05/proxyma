package proxyma

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"proxyma/internal/server"

	"github.com/spf13/cobra"
)

var (
	inviteExpire  int
	inviteStorage string
)

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Create an Invite Token for a new node to join the cluster",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrDie(inviteStorage)
		client := setupLocalAdminClient(cfg)
		reqPayload := server.InviteRequest{ValidForMinutes: 30}
		bodyBytes, _ := json.Marshal(reqPayload)
		url := fmt.Sprintf("%s/peers/invite", cfg.Address)
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(bodyBytes))

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			fmt.Println("❌ Error: couldn't connect to local server. Is it running?")
			os.Exit(1)
		}
		defer func() { _ = resp.Body.Close() }()

		var inviteResp server.InviteResponse
		_ = json.NewDecoder(resp.Body).Decode(&inviteResp)

		fmt.Println("✅ Invite Token generated successfully")
		fmt.Println("The invited node should execute:")
		fmt.Printf("\n  proxyma cluster join --token %s\n\n", inviteResp.Token)
	},
}

func init() {
	rootCmd.AddCommand(inviteCmd)
	defaultStorage := getDefaultStorage()
	inviteCmd.Flags().IntVar(&inviteExpire, "expire", 15, "Time for the Invite Token to expire (in minutes)")
	inviteCmd.Flags().StringVar(&inviteStorage, "storage", defaultStorage, "Path to the directory of the node")
}
