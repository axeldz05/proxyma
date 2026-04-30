package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	runStorage 	 string
	runDebugMode bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts the Proxyma node using the local configuration",
	Run: func(cmd *cobra.Command, args []string) {
		var opts slog.HandlerOptions
		if runDebugMode {
			opts = slog.HandlerOptions{
				Level: slog.LevelDebug,
			}
		}
		logger := slog.New(slog.NewTextHandler(os.Stdout, &opts))		
		cfg, err := protocol.LoadConfig(runStorage)
		if err != nil {
			logger.Error("Configuration not found. Did you run 'proxyma init' or 'proxyma join' first?", "error", err)
			os.Exit(1)
		}
		cfg.Logger = logger
		logger.Info("Starting Proxyma node", "id", cfg.ID, "address", cfg.Address)

		cfg.StoragePath = runStorage
		cfg.CAPath = filepath.Join(runStorage, "certs", "ca.crt")

		certsDir := filepath.Dir(cfg.CAPath)
		nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
		nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

		serverTLS, clientTLS, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
		if err != nil {
			logger.Error("Failed to initialize mTLS", "error", err)
			os.Exit(1)
		}

		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLS,
			},
		}
		peerClient := p2p.NewHTTPPeerClient(httpClient)

		srv := server.New(cfg, peerClient)

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

		go func() {
			if err := srv.ListenAndServe(serverTLS); err != nil && err != http.ErrServerClosed {
				logger.Error("Critical server error", "error", err)
				os.Exit(1)
			}
		}()
		if cfg.BootstrapNode != "" {
			go func() {
				time.Sleep(2 * time.Second) 
				logger.Info("Announcing presence to bootstrap node...", "sponsor", cfg.BootstrapNode)
				err := srv.AnnouncePresence(cfg.BootstrapNode)
				if err != nil {
					logger.Warn("Failed to announce to bootstrap node", "error", err)
				}
			}()
		}

		<-stop
		logger.Info("Initiating graceful shutdown...")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("Failure during shutdown", "error", err)
			os.Exit(1)
		}

		logger.Info("Node stopped successfully.")
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	runCmd.Flags().StringVar(&runStorage, "storage", defaultStorage, "Path to the node's anchor directory")
	runCmd.Flags().BoolVar(&runDebugMode, "debug", false, "Show debug logs")
}
