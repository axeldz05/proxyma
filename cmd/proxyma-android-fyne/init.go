package main

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func createInitialConfig(w fyne.Window, port string) {
	nid := utils.GenerateDefaultNodeID()
	localIP := getLocalIP()
	localAddr := fmt.Sprintf("https://%s:%s", localIP, port)
	if err := p2p.SetupNewNode(appStorage, nid, localAddr); err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
	}
}

func loadConfigOrDie(storagePath string, w fyne.Window) protocol.NodeConfig {
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return protocol.NodeConfig{}
	}
	return cfg
}
