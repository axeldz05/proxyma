package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

func joinCluster(tokenEntry *widget.Entry, w fyne.Window, nidEntry *widget.Entry, portEntry *widget.Entry, refreshUI func()) func() {
	return func() {
		go func() {
			logDebug := func(msg string, err error) {
				if appLogger != nil {
					if err != nil {
						appLogger.Error(msg, "error", err)
					} else {
						appLogger.Info(msg)
					}
				} else {
					if err != nil {
						fmt.Printf("time=%s level=ERROR msg=%q error=%q\n", time.Now().Format(time.RFC3339), msg, err.Error())
					} else {
						fmt.Printf("time=%s level=INFO msg=%q\n", time.Now().Format(time.RFC3339), msg)
					}
				}
			}

			logDebug("Join Cluster button pressed, loading configuration...", nil)
			cfg := loadConfigOrDie(appStorage, w)
			token := tokenEntry.Text
			nid := nidEntry.Text
			port := portEntry.Text

			logDebug(fmt.Sprintf("Input values captured: token_len=%d, node_id=%q, port=%q", len(token), nid, port), nil)

			token = strings.TrimSpace(token)
			token = strings.Trim(token, "\"'")
			if token == "" {
				logDebug("Join failed: Smart Token is empty", nil)
				fyne.Do(func() {
					dialog.ShowError(errors.New("Smart Token is required"), w)
				})
				return
			}
			if nid == "" {
				nid = utils.GenerateDefaultNodeID()
				logDebug(fmt.Sprintf("Auto-generated NodeID: %q", nid), nil)
			}

			localIP := getLocalIP()
			localAddr := fmt.Sprintf("https://%s:%s", localIP, port)
			logDebug(fmt.Sprintf("Local pairing address calculated: %s", localAddr), nil)

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			caCert, cert, privKeyPEM, successfulAddr, err := p2p.JoinCluster(ctx, token, nid, localAddr, logDebug)
			if err != nil {
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
				return
			}

			certsDir := filepath.Join(appStorage, "certs")
			logDebug(fmt.Sprintf("Purging certs folder: %s", certsDir), nil)
			_ = os.RemoveAll(certsDir)
			_ = os.MkdirAll(certsDir, 0755)

			caPath := filepath.Join(certsDir, "ca.crt")
			certPath := filepath.Join(certsDir, fmt.Sprintf("%s.crt", nid))
			keyPath := filepath.Join(certsDir, fmt.Sprintf("%s.key", nid))

			logDebug("Saving certificates to disk...", nil)
			err1 := os.WriteFile(caPath, []byte(caCert), 0644)
			err2 := os.WriteFile(certPath, []byte(cert), 0644)
			err3 := os.WriteFile(keyPath, privKeyPEM, 0600)
			if err1 != nil || err2 != nil || err3 != nil {
				logDebug("Failed writing certificate files to disk", fmt.Errorf("ca: %v, cert: %v, key: %v", err1, err2, err3))
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("Failed writing cert files to directory %s:\nca.crt: %v\ncert: %v\nkey: %v", certsDir, err1, err2, err3), w)
				})
				return
			}
			logDebug("Certificates saved to disk successfully.", nil)

			newCfg := protocol.NodeConfig{
				ID:            nid,
				Address:       localAddr,
				StoragePath:   appStorage,
				Workers:       cfg.Workers,
				CAPath:        caPath,
				BootstrapNode: strings.Replace(successfulAddr, "0.0.0.0", "node-1", 1),
			}
			logDebug(fmt.Sprintf("Saving node config to %s...", appStorage), nil)
			err = protocol.SaveConfig(newCfg)
			if err != nil {
				logDebug("Failed to save node config", err)
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("Failed saving config to %s:\n%w", appStorage, err), w)
				})
				return
			}
			logDebug("Node config saved successfully.", nil)
			fyne.Do(func() {
				dialog.ShowInformation("Success", "Joined cluster successfully", w)
				startNode()
				refreshUI()
				go func() {
					s := getRunningServer()
					if s != nil {
						_ = s.ExecuteSync()
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						for peerID := range s.GetPeersCopy() {
							_, _ = s.DiscoverServices(ctx, peerID)
						}
						fyne.Do(refreshUI)
					}
				}()
			})
		}()
	}
}
