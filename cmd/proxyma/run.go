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
	runDebugMode         bool
	runStartNode         = proxyma_bind.StartNode
	runStopNodeWithError = proxyma_bind.StopNodeWithError
	runWaitForSignal     = waitForRunSignal
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts the Proxyma node using the local configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		errStr := runStartNode(cliStorage, runDebugMode)
		if errStr != "" {
			return fmt.Errorf("error starting node: %s", proxyma_bind.ParseBindError(errStr))
		}

		runWaitForSignal()

		fmt.Println("Initiating graceful shutdown...")
		if stopResult := runStopNodeWithError(); stopResult != "" {
			return fmt.Errorf("error stopping node: %s", proxyma_bind.ParseBindError(stopResult))
		}
		fmt.Println("Node stopped successfully.")
		return nil
	},
}

func waitForRunSignal() {
	signal.Ignore(syscall.SIGHUP)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	<-stop
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().BoolVar(&runDebugMode, "debug", false, "Show debug logs")
}
