package cmd

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"log/slog"
	"net/http"
	"os"
)

var (
	syncTarget   string
	syncPeerID   string
	syncPeerAddr string
	syncCert     string
	syncKey      string
	syncCA       string
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Fuerza la sincronización de un nodo local con otro par",
	Long:  `Envía una petición POST a un nodo administrado por ti para que inicie inmediatamente un proceso de sincronización (P2P pull) con el par especificado en los flags.`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		payload := map[string]string{
			syncPeerID: syncPeerAddr,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			logger.Error("Failed to marshal JSON payload", "error", err)
			os.Exit(1)
		}

		caCertPool := x509.NewCertPool()
		if syncCA != "" {
			caCertPEM, err := os.ReadFile(syncCA)
			if err != nil {
				logger.Error("Failed to read CA certificate", "error", err)
				os.Exit(1)
			}
			if !caCertPool.AppendCertsFromPEM(caCertPEM) {
				logger.Error("Failed to append CA cert to pool")
				os.Exit(1)
			}
		}

		tr := &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		}

		if syncCert != "" && syncKey != "" {
			clientCert, err := tls.LoadX509KeyPair(syncCert, syncKey)
			if err != nil {
				logger.Error("Failed to load client certificate", "error", err)
				os.Exit(1)
			}
			tr.TLSClientConfig.Certificates = []tls.Certificate{clientCert}
		}

		client := &http.Client{Transport: tr}

		url := fmt.Sprintf("%s/sync", syncTarget)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			logger.Error("Failed to create request", "error", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")

		logger.Info("Triggering manual sync...", "target", url, "peer", syncPeerID)

		resp, err := client.Do(req)
		if err != nil {
			logger.Error("Failed to reach target node", "error", err)
			os.Exit(1)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Info("Sync triggered successfully", "status", resp.Status, "response", string(respBody))
		} else {
			logger.Error("Target node rejected sync request", "status", resp.Status, "response", string(respBody))
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().StringVar(&syncTarget, "target", "https://localhost:8083", "Dirección del nodo que va a RECIBIR la orden de sincronizar")
	syncCmd.Flags().StringVar(&syncPeerID, "peer-id", "node-1", "ID del nodo remoto a contactar")
	syncCmd.Flags().StringVar(&syncPeerAddr, "peer-addr", "https://localhost:8081", "Dirección P2P del nodo remoto a contactar")
	syncCmd.Flags().StringVar(&syncCA, "ca", "", "Ruta al certificado de la CA (ca.crt)")
	syncCmd.Flags().StringVar(&syncCert, "cert", "", "Ruta al certificado del cliente (mTLS)")
	syncCmd.Flags().StringVar(&syncKey, "key", "", "Ruta a la llave privada del cliente (mTLS)")
}
