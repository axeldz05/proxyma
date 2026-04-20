package cmd

import (
	"fmt"
	"os"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "proxyma",
	Short: "Proxyma es un motor de cómputo y almacenamiento distribuido P2P",
	Long: `Un clúster P2P seguro y distribuido.
Permite sincronizar archivos y ejecutar tareas de cómputo entre nodos cifrados con TLS mutuo.`,
	// Run: func(cmd *cobra.Command, args []string) { } // Podrías poner algo aquí si quisieras
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
