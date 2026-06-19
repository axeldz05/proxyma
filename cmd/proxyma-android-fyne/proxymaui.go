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

			lbl := widget.NewLabel(fmt.Sprintf("%s (v%d, %s, %s)", fileEntry.Name, fileEntry.Version, byteCountSI(fileEntry.Size), statusText))
			subBtn := widget.NewButton("Subscribe", func() {
				srv.Storage.SetSubscription(fileEntry.Name, true)
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

			row := container.NewHBox(lbl, subBtn, exportBtn)
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
				ui.servicesListContainer.Objects = nil
				for name := range names {
					svcName := name
					btn := widget.NewButton(svcName, func() {
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
		ui.statusLabel.SetText("Status: Couldn't run")
		ui.nodeIDLabel.SetText("Node ID: -")
		ui.nodeAddrLabel.SetText("Address: -")
		ui.peersContainer.Objects = nil
		ui.vfsFilesContainer.Objects = nil
		return
	}

	ui.statusLabel.SetText("Status: Online")
	ui.nodeIDLabel.SetText("Node ID: " + srv.Config.ID)
	ui.nodeAddrLabel.SetText("Address: " + srv.Config.Address)

	ui.peersContainer.Objects = nil
	for id, addr := range srv.GetPeersCopy() {
		ui.peersContainer.Add(widget.NewLabel(fmt.Sprintf("%s (%s)", id, addr)))
	}
	ui.peersContainer.Refresh()

	ui.vfsFilesContainer.Objects = nil
}
