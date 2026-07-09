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

var peersStorage string

var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "View connected cluster peers and status",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = loadConfigOrDie(peersStorage)
		proxyma_bind.SetStoragePath(peersStorage)

		jsonStr := proxyma_bind.GetPeersJson()
		if strings.Contains(jsonStr, `"error":`) {
			type ErrResp struct {
				Error string `json:"error"`
			}
			var errR ErrResp
			_ = json.Unmarshal([]byte(jsonStr), &errR)
			return fmt.Errorf("%s", errR.Error)
		}

		var peers []protocol.PeerStatus
		if err := json.Unmarshal([]byte(jsonStr), &peers); err != nil {
			return fmt.Errorf("failed to parse peers: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(w, "PEER ID\tADDRESS\tSTATUS\tERROR")

		if len(peers) == 0 {
			fmt.Println("No peers registered in the cluster.")
			return nil
		}

		for _, p := range peers {
			statusStr := "ONLINE"
			if !p.Online {
				statusStr = "OFFLINE"
			}
			errStr := "-"
			if p.Error != "" {
				errStr = p.Error
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Address, statusStr, errStr)
		}

		_ = w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(peersCmd)
	defaultStorage := getDefaultStorage()
	peersCmd.Flags().StringVar(&peersStorage, "storage", defaultStorage, "Path to the directory of the node")
}
