package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"time"

	"github.com/spf13/cobra"
)

var (
	inviteExpire 	int
	inviteStorage 	string
)

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Create an Invite Token for a new node to join the cluster",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := protocol.LoadConfig(inviteStorage)
		if err != nil {
			fmt.Println("❌ Error: couldn't find config.json. Did you run 'proxyma run' first?")
			os.Exit(1)
		}
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
		defer func() {_ = resp.Body.Close()}()

		var inviteResp server.InviteResponse
		_ = json.NewDecoder(resp.Body).Decode(&inviteResp)

		fmt.Println("✅ Invite Token generated successfully")
		fmt.Println("The invited node should execute:")
		fmt.Printf("\n  proxyma cluster join --token %s\n\n", inviteResp.Token)
	},
}

func init() {
	rootCmd.AddCommand(inviteCmd)
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	inviteCmd.Flags().IntVar(&inviteExpire, "expire", 15, "Time for the Invite Token to expire (in minutes)")
	inviteCmd.Flags().StringVar(&inviteStorage, "storage", defaultStorage, "Path to the directory of the node")
	_ = runCmd.MarkFlagRequired("id")
}

func setupLocalAdminClient(cfg protocol.NodeConfig) *http.Client {
	caCertFile := filepath.Join(filepath.Dir(cfg.CAPath), "ca.crt")
	nodeCertFile := filepath.Join(filepath.Dir(cfg.CAPath), cfg.ID+".crt")
	nodeKeyFile := filepath.Join(filepath.Dir(cfg.CAPath), cfg.ID+".key")

	_, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	if err != nil {
		fmt.Printf("❌ Error loading local certificates: %v\n", err)
		os.Exit(1)
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
		Timeout: 5 * time.Second,
	}
}
