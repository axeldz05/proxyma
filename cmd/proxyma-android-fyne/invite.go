package main

import (
	"errors"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"proxyma/internal/p2p"
)

func generateInviteToken(w fyne.Window, inviteTokenEntry *widget.Entry) func() {
	return func() {
		s := getRunningServer()
		if s == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		expiration := time.Now().Add(15 * time.Minute)
		s.Config.Logger.Info("Token generated in UI", "expires", expiration)
		s.AddPendingInvite(secretHex, expiration)
		inviteTokenEntry.SetText(smartToken)
	}
}
