package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"proxyma/internal/server"
)

func saveReaderToVFS(w fyne.Window, s *server.Server, name string, r io.Reader, onSuccess func()) {
	err := s.Storage.SaveLocalFile(name, r)
	if err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return
	}
	if onSuccess != nil {
		fyne.Do(onSuccess)
	}
}

func changeVFSStorageLocation(a fyne.App, w fyne.Window, refreshUI func()) func() {
	return func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			newPath := uri.Path()
			if newPath == "" {
				return
			}

			newStorage := filepath.Join(newPath, "proxyma_data")

			go func() {
				stopNode()

				if _, err := os.Stat(appStorage); err == nil {
					err = copyDir(appStorage, newStorage)
					if err != nil {
						fyne.Do(func() {
							dialog.ShowError(fmt.Errorf("failed to copy data: %v", err), w)
						})
						_ = startNode()
						fyne.Do(refreshUI)
						return
					}
				}

				oldStorage := appStorage
				appStorage = newStorage

				a.Preferences().SetString("app_storage_path", appStorage)

				err = startNode()
				if err != nil {
					fyne.Do(func() {
						dialog.ShowError(fmt.Errorf("failed to start node on new storage: %v", err), w)
					})
					appStorage = oldStorage
					a.Preferences().SetString("app_storage_path", appStorage)
					_ = startNode()
				} else {
					fyne.Do(func() {
						dialog.ShowInformation("Storage Relocated", fmt.Sprintf("Storage moved to %s. Node restarted.", appStorage), w)
					})
				}
				fyne.Do(refreshUI)
			}()
		}, w)
	}
}
