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
	runDebugMode bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts the Proxyma node using the local configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		errStr := proxyma_bind.StartNode(cliStorage, runDebugMode)
		if errStr != "" {
			return fmt.Errorf("error starting node: %s", proxyma_bind.ParseBindError(errStr))
		}

		signal.Ignore(syscall.SIGHUP)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop

		fmt.Println("Initiating graceful shutdown...")
		proxyma_bind.StopNode()
		fmt.Println("Node stopped successfully.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().BoolVar(&runDebugMode, "debug", false, "Show debug logs")
}
