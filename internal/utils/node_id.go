package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// GenerateDefaultNodeID generates a unique node ID using the system hostname and a short random hex.
func GenerateDefaultNodeID() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "node"
	}
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-0000", hostname)
	}
	return fmt.Sprintf("%s-%s", hostname, hex.EncodeToString(b))
}
