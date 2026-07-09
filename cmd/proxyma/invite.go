package main

import (
	"fmt"
	"os"
	"strings"

	proxyma_bind "proxyma/cmd/proxyma-bind"

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
		_ = loadConfigOrDie(inviteStorage)
		proxyma_bind.SetStoragePath(inviteStorage)

		token := proxyma_bind.GenerateInviteToken()
		if token == "" || strings.HasPrefix(token, "error:") {
			fmt.Printf("❌ Error generating invite: %s\n", token)
			os.Exit(1)
		}

		fmt.Println("✅ Invite Token generated successfully")
		fmt.Println("The invited node should execute:")
		fmt.Printf("\n  proxyma cluster join --token %s\n\n", token)
	},
}

func init() {
	rootCmd.AddCommand(inviteCmd)
	defaultStorage := getDefaultStorage()
	inviteCmd.Flags().IntVar(&inviteExpire, "expire", 15, "Time for the Invite Token to expire (in minutes)")
	inviteCmd.Flags().StringVar(&inviteStorage, "storage", defaultStorage, "Path to the directory of the node")
}
