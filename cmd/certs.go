package cmd

import (
	"log/slog"
	"os"
	"proxyma/internal/p2p"

	"github.com/spf13/cobra"
)

var (
	// Flags for 'init'
	initPath string

	// Flags for 'issue'
	issueCA       string
	issueNodePath string
	issueNodeID   string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Inicializa la Autoridad Certificadora (CA) del clúster",
	Long:  `Crea un nuevo par de llaves criptográficas (ca.crt y ca.key) que servirán como la raíz de confianza para todos los nodos del clúster Proxyma.`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		if err := p2p.InitCluster(initPath); err != nil {
			logger.Error("Failed to initialize cluster CA", "error", err)
			os.Exit(1)
		}
		logger.Info("Cluster CA initialized successfully", "path", initPath)
	},
}

var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Emite un certificado TLS para un nuevo nodo",
	Long:  `Firma un nuevo certificado utilizando la CA del clúster. El certificado generado contendrá el ID del nodo en su CommonName y DNSNames para asegurar conexiones P2P.`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		if err := p2p.IssueNodeCertificate(issueCA, issueNodePath, issueNodeID); err != nil {
			logger.Error("Failed to issue node certificate", "error", err, "nodeID", issueNodeID)
			os.Exit(1)
		}
		logger.Info("Certificate issued successfully", "nodeID", issueNodeID, "path", issueNodePath)
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(issueCmd)

	// init flags
	initCmd.Flags().StringVar(&initPath, "path", "./certs", "Directorio donde se guardarán ca.crt y ca.key")

	// issue flags
	issueCmd.Flags().StringVar(&issueCA, "ca", "./certs", "Directorio donde se encuentra la CA")
	issueCmd.Flags().StringVar(&issueNodePath, "node-path", "./certs", "Directorio donde se guardarán los certificados emitidos")
	issueCmd.Flags().StringVar(&issueNodeID, "id", "", "ID único del nodo para el cual se emite el certificado")

	_ = issueCmd.MarkFlagRequired("id")
}
