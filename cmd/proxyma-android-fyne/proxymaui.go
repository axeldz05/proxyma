package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"time"

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
}

func (ui *ProxymaUI) SyncVFS(w fyne.Window) func() {
	return func() {
		srvMutex.Lock()
		s := srv
		srvMutex.Unlock()
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
		srvMutex.Lock()
		s := srv
		srvMutex.Unlock()
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
			err = s.Storage.SaveLocalFile(name, reader)
			if err != nil {
				dialog.ShowError(err, w)
			} else {
				dialog.ShowInformation("Uploaded", fmt.Sprintf("File %s uploaded successfully", name), w)
				ui.Refresh()
			}
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

			sentSpeed, recvSpeed := srv.GetCategoryBandwidth("vfs:" + fileEntry.Hash)
			speedInfo := ""
			if sentSpeed > 0 || recvSpeed > 0 {
				speedInfo = fmt.Sprintf(" [Up: %.1f KB/s, Down: %.1f KB/s]", sentSpeed/1024.0, recvSpeed/1024.0)
			}

			lbl := widget.NewLabel(fmt.Sprintf("%s (v%d, %s, %s)%s", fileEntry.Name, fileEntry.Version, byteCountSI(fileEntry.Size), statusText, speedInfo))
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

			exportBtn := widget.NewButton("Export", func() {
				if !hasLocal {
					dialog.ShowError(errors.New("File is not present locally. Subscribe first."), w)
					return
				}
				dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
					if err != nil || writer == nil {
						return
					}
					defer writer.Close()
					err = srv.Storage.ReadPhysicalBlob(fileEntry.Hash, writer)
					if err != nil {
						dialog.ShowError(err, w)
					} else {
						dialog.ShowInformation("Exported", "File exported successfully", w)
					}
				}, w)
			})

			deleteBtn := widget.NewButton("Delete Local", func() {
				err := srv.Storage.DeleteLocalCache(fileEntry.Name)
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					ui.Refresh()
				}
			})

			row := container.NewHBox(lbl, subBtn, exportBtn, deleteBtn)
			ui.vfsFilesContainer.Add(row)
		}
		ui.vfsFilesContainer.Refresh()
	}
}

func (ui *ProxymaUI) DiscoverServices(w fyne.Window) func() {
	return func() {
		srvMutex.Lock()
		s := srv
		srvMutex.Unlock()
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
				ui.servicesListContainer.Objects = []fyne.CanvasObject{}
				for name := range names {
					svcName := name
					sentSpeed, recvSpeed := s.GetCategoryBandwidth("service:" + svcName)
					labelStr := svcName
					if sentSpeed > 0 || recvSpeed > 0 {
						labelStr = fmt.Sprintf("%s [Up: %.1f KB/s, Down: %.1f KB/s]", svcName, sentSpeed/1024.0, recvSpeed/1024.0)
					}
					btn := widget.NewButton(labelStr, func() {
						loadServiceDetails(svcName, w, ui.serviceDetailContainer)
					})
					ui.servicesListContainer.Add(btn)
				}
				ui.servicesListContainer.Refresh()
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
		ui.servicesListContainer.Objects = []fyne.CanvasObject{}
		names := make(map[string]bool)
		for _, name := range srv.Compute.ListServices() {
			names[name] = true
		}
		for peerID := range srv.GetPeersCopy() {
			for name := range srv.GetClusterServices(peerID) {
				names[name] = true
			}
		}

		for name := range names {
			svcName := name
			sentSpeed, recvSpeed := srv.GetCategoryBandwidth("service:" + svcName)
			labelStr := svcName
			if sentSpeed > 0 || recvSpeed > 0 {
				labelStr = fmt.Sprintf("%s [Up: %.1f KB/s, Down: %.1f KB/s]", svcName, sentSpeed/1024.0, recvSpeed/1024.0)
			}
			btn := widget.NewButton(labelStr, func() {
				loadServiceDetails(svcName, ui.window, ui.serviceDetailContainer)
			})
			ui.servicesListContainer.Add(btn)
		}
		ui.servicesListContainer.Refresh()
	}
}
