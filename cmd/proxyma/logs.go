package main

import (
	"encoding/json"
	"fmt"
	"strings"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

var (
	logsStorage string
	logsLimit   int
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View logs from the running daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = loadConfigOrDie(logsStorage)
		proxyma_bind.SetStoragePath(logsStorage)

		jsonStr := proxyma_bind.GetLogsJson()
		if strings.Contains(jsonStr, `"error":`) {
			type ErrResp struct {
				Error string `json:"error"`
			}
			var errR ErrResp
			_ = json.Unmarshal([]byte(jsonStr), &errR)
			return fmt.Errorf("%s", errR.Error)
		}

		var logs []protocol.LogRecord
		if err := json.Unmarshal([]byte(jsonStr), &logs); err != nil {
			return fmt.Errorf("failed to parse logs: %w", err)
		}

		if len(logs) == 0 {
			fmt.Println("No logs buffered.")
			return nil
		}

		// Apply limit if specified
		startIdx := 0
		if logsLimit > 0 && len(logs) > logsLimit {
			startIdx = len(logs) - logsLimit
		}

		for i := startIdx; i < len(logs); i++ {
			fmt.Printf("[%s] [%s] %s\n", logs[i].Timestamp, logs[i].Level, logs[i].Message)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	defaultStorage := getDefaultStorage()
	logsCmd.Flags().StringVar(&logsStorage, "storage", defaultStorage, "Path to the directory of the node")
	logsCmd.Flags().IntVar(&logsLimit, "limit", 0, "Limit the number of log lines displayed (0 for all available)")
}
