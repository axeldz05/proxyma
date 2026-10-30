package main

import (
	"fmt"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

// getDefaultStorage returns the default storage path from environment or home directory fallback.
func getDefaultStorage() string {
	return protocol.DefaultStoragePath()
}

// requireConfig fails when the node has no config.json yet. Returning the error
// lets cobra print it and set the exit code; helpers must not kill the process.
func requireConfig(storagePath string) error {
	if _, err := protocol.LoadConfig(storagePath); err != nil {
		return fmt.Errorf("couldn't find config.json in %s: run 'proxyma init' or 'proxyma cluster join' first", storagePath)
	}
	return nil
}

// generateDefaultNodeID generates a fallback node ID using hostname and a short random hex
func generateDefaultNodeID() string {
	return utils.GenerateDefaultNodeID()
}
