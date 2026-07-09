package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

var statsStorage string

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "View real-time bandwidth speeds and totals",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = loadConfigOrDie(statsStorage)
		proxyma_bind.SetStoragePath(statsStorage)

		jsonStr := proxyma_bind.GetBandwidthStatsJson()
		if strings.Contains(jsonStr, `"error":`) {
			type ErrResp struct {
				Error string `json:"error"`
			}
			var errR ErrResp
			_ = json.Unmarshal([]byte(jsonStr), &errR)
			return fmt.Errorf("%s", errR.Error)
		}

		var stats protocol.BandwidthStats
		if err := json.Unmarshal([]byte(jsonStr), &stats); err != nil {
			return fmt.Errorf("failed to parse stats: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(w, "METRIC\tVALUE")

		formatSpeed := func(bps int64) string {
			if bps >= 1024*1024 {
				return fmt.Sprintf("%.2f MB/s", float64(bps)/(1024*1024))
			} else if bps >= 1024 {
				return fmt.Sprintf("%.2f KB/s", float64(bps)/1024)
			}
			return fmt.Sprintf("%d B/s", bps)
		}

		formatSize := func(bytes int64) string {
			if bytes >= 1024*1024*1024 {
				return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
			} else if bytes >= 1024*1024 {
				return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
			} else if bytes >= 1024 {
				return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
			}
			return fmt.Sprintf("%d B", bytes)
		}

		_, _ = fmt.Fprintf(w, "Download Speed\t%s\n", formatSpeed(stats.DownloadSpeed))
		_, _ = fmt.Fprintf(w, "Upload Speed\t%s\n", formatSpeed(stats.UploadSpeed))
		_, _ = fmt.Fprintf(w, "Total Received\t%s\n", formatSize(stats.TotalReceived))
		_, _ = fmt.Fprintf(w, "Total Sent\t%s\n", formatSize(stats.TotalSent))

		_ = w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statsCmd)
	defaultStorage := getDefaultStorage()
	statsCmd.Flags().StringVar(&statsStorage, "storage", defaultStorage, "Path to the directory of the node")
}
