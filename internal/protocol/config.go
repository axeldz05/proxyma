package protocol

import (
	"path/filepath"

	"proxyma/internal/utils"
)

// configFileName is where a node's NodeConfig lives inside its storage dir.
const configFileName = "config.json"

func SaveConfig(cfg NodeConfig) error {
	return utils.WriteJSONFile(filepath.Join(cfg.StoragePath, configFileName), cfg)
}

func LoadConfig(storagePath string) (NodeConfig, error) {
	var cfg NodeConfig
	err := utils.ReadJSONFile(filepath.Join(storagePath, configFileName), &cfg)
	return cfg, err
}
