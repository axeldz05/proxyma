package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"time"

	"proxyma/internal/server"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)


type ProxymaUI struct {
	window                 fyne.Window
	statusLabel            *widget.Label
	nodeIDLabel            *widget.Label
	nodeAddrLabel          *widget.Label
	uploadSpeedLabel       *widget.Label
	downloadSpeedLabel     *widget.Label
	peersContainer         *fyne.Container
	vfsFilesContainer      *fyne.Container
	servicesListContainer  *fyne.Container
	serviceDetailContainer *fyne.Container
	showInfo               bool
	showWarn               bool
	showError              bool
	logsContainer          *fyne.Container
}

func (ui *ProxymaUI) SyncVFS(w fyne.Window) func() {
	return func() {
		s := getRunningServer()
		if s == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		go func() {
			err := s.ExecuteSync()
			if err != nil {
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
				return
			}
			fyne.Do(ui.Refresh)
		}()
	}
}
func (ui *ProxymaUI) UploadFile(w fyne.Window) func() {
	return func() {
		s := getRunningServer()
		if s == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil || reader == nil {
				return
			}
			defer reader.Close()
			name := reader.URI().Name()
			saveReaderToVFS(w, s, name, reader, func() {
				dialog.ShowInformation("Uploaded", fmt.Sprintf("File %s uploaded successfully", name), w)
				ui.Refresh()
			})
		}, w)
	}
}

func (ui *ProxymaUI) updateVFSSnapshot(w fyne.Window) func() {
	return func() {
		ui.vfsFilesContainer.Objects = []fyne.CanvasObject{}
		for _, entry := range srv.Storage.GetVFSSnapshot() {
			fileEntry := entry
			if fileEntry.Deleted {
				continue
			}

			hasLocal, _ := srv.Storage.HasPhysicalBlob(fileEntry.Hash)
			statusText := "Remote"
			if hasLocal {
				statusText = "Local"
			}

			isSubscribed := srv.Storage.IsSubscribed(fileEntry.Name)
			subText := "Subscribe"
			if isSubscribed {
				subText = "Unsubscribe"
			}

			suffix := formatBandwidthSuffix("vfs:"+fileEntry.Hash, srv)
			lbl := widget.NewLabel(fmt.Sprintf("%s (v%d, %s, %s)%s", fileEntry.Name, fileEntry.Version, byteCountSI(fileEntry.Size), statusText, suffix))
			subBtn := widget.NewButton(subText, func() {
				srv.Storage.SetSubscription(fileEntry.Name, !isSubscribed)
				if !isSubscribed {
					go func() {
						_ = srv.ExecuteSync()
						ui.Refresh()
					}()
				}
				ui.Refresh()
			})

			openBtn := widget.NewButton("Open", func() {
				openLocalVFSPath(w, fileEntry.Hash, hasLocal, false)
			})

			openLocBtn := widget.NewButton("Open Location", func() {
				openLocalVFSPath(w, fileEntry.Hash, hasLocal, true)
			})

			deleteBtn := widget.NewButton("Delete Local", func() {
				err := srv.Storage.DeleteLocalCache(fileEntry.Name)
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					ui.Refresh()
				}
			})

			row := container.NewHBox(lbl, subBtn, openBtn, openLocBtn, deleteBtn)
			ui.vfsFilesContainer.Add(row)
		}
		ui.vfsFilesContainer.Refresh()
	}
}

func (ui *ProxymaUI) DiscoverServices(w fyne.Window) func() {
	return func() {
		s := getRunningServer()
		if s == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		go func() {
			names := make(map[string]bool)
			for _, name := range s.Compute.ListServices() {
				names[name] = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for peerID := range s.GetPeersCopy() {
				peerSvc, err := s.DiscoverServices(ctx, peerID)
				if err == nil {
					for _, name := range peerSvc {
						names[name] = true
					}
				}
			}

			fyne.Do(func() {
				ui.updateServicesList(w, names)
			})
		}()
	}
}

func (ui *ProxymaUI) Refresh() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! PANIC ATRAPADO !!!\nError: %v\nStack Trace:\n%s", r, string(debug.Stack()))
		}
		srvMutex.Unlock()
	}()
	srvMutex.Lock()

	if srv == nil {
		if ui.statusLabel != nil { ui.statusLabel.SetText("Status: Couldn't run") }
		if ui.nodeIDLabel != nil { ui.nodeIDLabel.SetText("Node ID: -") }
		if ui.nodeAddrLabel != nil { ui.nodeAddrLabel.SetText("Address: -") }
		if ui.uploadSpeedLabel != nil { ui.uploadSpeedLabel.SetText("Upload Speed: 0 B/s (Total: 0 B)") }
		if ui.downloadSpeedLabel != nil { ui.downloadSpeedLabel.SetText("Download Speed: 0 B/s (Total: 0 B)") }
		if ui.peersContainer != nil { ui.peersContainer.Objects = []fyne.CanvasObject{} }
		if ui.vfsFilesContainer != nil { ui.vfsFilesContainer.Objects = []fyne.CanvasObject{} }
		if ui.servicesListContainer != nil { ui.servicesListContainer.Objects = []fyne.CanvasObject{} }
		return
	}

	if ui.statusLabel != nil { ui.statusLabel.SetText("Status: Online") }
	if ui.nodeIDLabel != nil { ui.nodeIDLabel.SetText("Node ID: " + srv.Config.ID) }
	if ui.nodeAddrLabel != nil { ui.nodeAddrLabel.SetText("Address: " + srv.Config.Address) }

	upSpeed, downSpeed := srv.GetCurrentBandwidth()
	totalSent, totalRecv := srv.GetTotalBandwidth()
	if ui.uploadSpeedLabel != nil { ui.uploadSpeedLabel.SetText(fmt.Sprintf("Upload Speed: %s/s (Total: %s)", byteCountSI(int64(upSpeed)), byteCountSI(totalSent))) }
	if ui.downloadSpeedLabel != nil { ui.downloadSpeedLabel.SetText(fmt.Sprintf("Download Speed: %s/s (Total: %s)", byteCountSI(int64(downSpeed)), byteCountSI(totalRecv))) }

	if ui.peersContainer != nil {
		ui.peersContainer.Objects = []fyne.CanvasObject{}
		for id, addr := range srv.GetPeersCopy() {
			status := "Offline"
			if srv.IsPeerOnline(id) {
				status = "Online"
			}
			ui.peersContainer.Add(widget.NewLabel(fmt.Sprintf("%s (%s) [%s]", id, addr, status)))
		}
		ui.peersContainer.Refresh()
	}

	if ui.vfsFilesContainer != nil {
		ui.updateVFSSnapshot(ui.window)()
	}

	if ui.servicesListContainer != nil {
		names := make(map[string]bool)
		for _, name := range srv.Compute.ListServices() {
			names[name] = true
		}
		for peerID := range srv.GetPeersCopy() {
			for name := range srv.GetClusterServices(peerID) {
				names[name] = true
			}
		}
		ui.updateServicesList(ui.window, names)
	}
}

func (ui *ProxymaUI) refreshLogs() {
	if ui.logsContainer == nil {
		return
	}
	ui.logsContainer.Objects = []fyne.CanvasObject{}

	logBufferMu.Lock()
	records := make([]LogRecord, len(logBuffer))
	copy(records, logBuffer)
	logBufferMu.Unlock()

	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		if rec.Level == "INFO" && !ui.showInfo {
			continue
		}
		if rec.Level == "WARN" && !ui.showWarn {
			continue
		}
		if rec.Level == "ERROR" && !ui.showError {
			continue
		}

		prefix := "ℹ️ "
		if rec.Level == "ERROR" {
			prefix = "❌ "
		} else if rec.Level == "WARN" {
			prefix = "⚠️ "
		}

		ui.logsContainer.Add(widget.NewLabel(prefix + rec.Message))
	}
	ui.logsContainer.Refresh()
}

func formatBandwidthSuffix(category string, s *server.Server) string {
	sentSpeed, recvSpeed := s.GetCategoryBandwidth(category)
	if sentSpeed > 0 || recvSpeed > 0 {
		return fmt.Sprintf(" [Up: %.1f KB/s, Down: %.1f KB/s]", sentSpeed/1024.0, recvSpeed/1024.0)
	}
	return ""
}

func openLocalVFSPath(w fyne.Window, hash string, hasLocal bool, getDir bool) {
	if !hasLocal {
		dialog.ShowError(errors.New("File is not present locally. Subscribe first."), w)
		return
	}
	blobPath := srv.Storage.GetLocalBlobPath(hash)
	if getDir {
		blobPath = filepath.Dir(blobPath)
	}
	u, err := url.Parse("file://" + filepath.Clean(blobPath))
	if err != nil {
		dialog.ShowError(err, w)
		return
	}
	_ = fyne.CurrentApp().OpenURL(u)
}

func (ui *ProxymaUI) updateServicesList(w fyne.Window, names map[string]bool) {
	ui.servicesListContainer.Objects = []fyne.CanvasObject{}
	for name := range names {
		svcName := name
		labelStr := svcName + formatBandwidthSuffix("service:"+svcName, srv)
		btn := widget.NewButton(labelStr, func() {
			loadServiceDetails(svcName, w, ui.serviceDetailContainer)
		})
		ui.servicesListContainer.Add(btn)
	}
	ui.servicesListContainer.Refresh()
}
