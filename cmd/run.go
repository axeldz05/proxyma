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
	runID      string
	runAddr    string
	runStorage string
	runCA      string
	runCert    string
	runKey     string
	runWorkers int
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Inicia un nodo de Proxyma",
	Long:  `Levanta el servidor HTTP/P2P, inicializa los motores de almacenamiento y cómputo, y espera conexiones.`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		logger.Info("Starting Proxyma node", "id", runID, "address", runAddr)

		if runCert == "" {
			runCert = fmt.Sprintf("./certs/%s.crt", runID)
		}
		if runKey == "" {
			runKey = fmt.Sprintf("./certs/%s.key", runID)
		}

		if err := ensureDir(runStorage); err != nil {
			logger.Error("Initialization failed", "error", err)
			os.Exit(1)
		}
		logger.Info("Storage directory verified", "path", runStorage)

		serverTLS, clientTLS, err := p2p.LoadNodeTLS(runCA, runCert, runKey)
		if err != nil {
			logger.Error("Failed to initialize TLS", "error", err)
			os.Exit(1)
		}

		cfg := protocol.NodeConfig{
			ID:          runID,
			Address:     runAddr,
			StoragePath: runStorage,
			Workers:     runWorkers,
			Logger:      logger,
		}

		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLS,
			},
		}

		srv := server.New(cfg, httpClient)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

		go func() {
			if err := srv.ListenAndServe(serverTLS); err != nil && err != http.ErrServerClosed {
				logger.Error("Server encountered a critical error", "error", err)
				os.Exit(1)
			}
		}()

		<-stop

		logger.Info("Shutting down Proxyma node...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("Server shutdown failed", "error", err)
			os.Exit(1)
		}

		logger.Info("Proxyma node stopped successfully. Goodbye!")
	},
}

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVar(&runID, "id", "", "ID único del nodo (requerido)")
	runCmd.Flags().StringVar(&runAddr, "addr", "https://localhost:8080", "Dirección de escucha del nodo")
	runCmd.Flags().StringVar(&runStorage, "storage", "./data", "Ruta al directorio de blobs")
	runCmd.Flags().StringVar(&runCA, "ca", "./certs/ca.crt", "Ruta al certificado de la CA")
	runCmd.Flags().StringVar(&runCert, "cert", "", "Ruta al certificado del nodo (requerido)")
	runCmd.Flags().StringVar(&runKey, "key", "", "Ruta a la llave privada del nodo (requerido)")
	runCmd.Flags().IntVar(&runWorkers, "workers", 4, "Ruta a la llave privada del nodo (requerido)")

	_ = runCmd.MarkFlagRequired("id")
}

func ensureDir(path string) error {
	cleanPath := filepath.Clean(path)

	err := os.MkdirAll(cleanPath, 0755)
	if err != nil {
		return fmt.Errorf("could not create directory %s: %w", cleanPath, err)
	}
	
	return nil
}
