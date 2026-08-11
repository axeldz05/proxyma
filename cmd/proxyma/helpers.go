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

// requireConfig fails when the node has no config.json yet. Returning the error
// lets cobra print it and set the exit code; helpers must not kill the process.
func requireConfig(storagePath string) error {
	if _, err := protocol.LoadConfig(storagePath); err != nil {
		return fmt.Errorf("couldn't find config.json in %s: run 'proxyma init' or 'proxyma join' first", storagePath)
	}
	return nil
}

// generateDefaultNodeID generates a fallback node ID using hostname and a short random hex
func generateDefaultNodeID() string {
	return utils.GenerateDefaultNodeID()
}
