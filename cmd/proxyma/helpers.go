package main

import (
	"fmt"
	"os"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// getDefaultStorage returns the default storage path from environment or fallback
func getDefaultStorage() string {
	defaultStorage := os.Getenv("PROXYMA_STORAGE")
	if defaultStorage == "" {
		defaultStorage = "./data"
	}
	return defaultStorage
}

// loadConfigOrDie loads the NodeConfig or exits if not found
func loadConfigOrDie(storagePath string) protocol.NodeConfig {
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		fmt.Printf("❌ Error: Couldn't find config.json in %s. Run 'proxyma init' or 'proxyma join' first.\n", storagePath)
		os.Exit(1)
	}
	return cfg
}

// generateDefaultNodeID generates a fallback node ID using hostname and a short random hex
func generateDefaultNodeID() string {
	return utils.GenerateDefaultNodeID()
}
