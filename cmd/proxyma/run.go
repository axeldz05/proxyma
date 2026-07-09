package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	proxyma_bind "proxyma/cmd/proxyma-bind"

	"github.com/spf13/cobra"
)

var (
	runStorage   string
	runDebugMode bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts the Proxyma node using the local configuration",
	Run: func(cmd *cobra.Command, args []string) {
		errStr := proxyma_bind.StartNode(runStorage, runDebugMode)
		if errStr != "" {
			fmt.Printf("❌ Error starting node: %s\n", errStr)
			os.Exit(1)
		}

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop

		fmt.Println("Initiating graceful shutdown...")
		proxyma_bind.StopNode()
		fmt.Println("Node stopped successfully.")
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	defaultStorage := getDefaultStorage()
	runCmd.Flags().StringVar(&runStorage, "storage", defaultStorage, "Path to the node's anchor directory")
	runCmd.Flags().BoolVar(&runDebugMode, "debug", false, "Show debug logs")
}
