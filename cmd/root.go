package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "proxyma",
	Short: "Proxyma is a distributed P2P compute and storage engine",
	Long: `A secure and distributed P2P cluster.
Allows synchronizing files and running compute tasks between nodes encrypted with mutual TLS.`,
	// Run: func(cmd *cobra.Command, args []string) { } // You could put something here if you wanted to
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
