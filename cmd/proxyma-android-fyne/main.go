package main

import (
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

var (
	srv        *server.Server
	srvTLS     *tls.Config
	srvMutex   sync.Mutex
	appStorage string
	appLogger  *slog.Logger
)

func main() {
	a := app.NewWithID("com.proxyma.android")
	w := a.NewWindow("Proxyma Android")
	ui := &ProxymaUI{
		window:                 w,
		statusLabel:            widget.NewLabel("Status: Offline"),
		nodeIDLabel:            widget.NewLabel("Node ID: -"),
		nodeAddrLabel:          widget.NewLabel("Address: -"),
		uploadSpeedLabel:       widget.NewLabel("Upload Speed: 0 B/s (Total: 0 B)"),
		downloadSpeedLabel:     widget.NewLabel("Download Speed: 0 B/s (Total: 0 B)"),
		peersContainer:         container.NewVBox(),
		vfsFilesContainer:      container.NewVBox(),
		servicesListContainer:  container.NewVBox(),
		serviceDetailContainer: container.NewVBox(),
	}
	tokenEntry := widget.NewEntry()
	nodeIDEntry := widget.NewEntry()
	portEntry := widget.NewEntry()
	portEntry.SetText("8080")
	tokenEntry.SetPlaceHolder("Smart Token")
	nodeIDEntry.SetPlaceHolder("Node ID (optional)")

	inviteTokenEntry := widget.NewEntry()
	inviteTokenEntry.SetPlaceHolder("Generated smart token will appear here")
	generateInviteBtn := widget.NewButton("Generate Invite Token", generateInviteToken(w, inviteTokenEntry))
	joinBtn := widget.NewButton("Join Cluster", joinCluster(tokenEntry.Text, w, nodeIDEntry.Text, portEntry.Text, ui.Refresh))

	statusCard := widget.NewCard("Node Status", "", container.NewVBox(
		ui.statusLabel,
		ui.nodeIDLabel,
		ui.nodeAddrLabel,
		ui.uploadSpeedLabel,
		ui.downloadSpeedLabel,
	))

	inviteCard := widget.NewCard("Invite Peer", "", container.NewVBox(
		generateInviteBtn,
		inviteTokenEntry,
	))

	peersCard := widget.NewCard("Active Peers", "", ui.peersContainer)

	tabStatus := container.NewScroll(container.NewVBox(
		statusCard,
		inviteCard,
		peersCard,
	))

	tabPairing := container.NewVBox(
		widget.NewLabel("Join Existing Cluster"),
		tokenEntry,
		nodeIDEntry,
		portEntry,
		joinBtn,
	)

	syncBtn := widget.NewButton("Sync VFS", ui.SyncVFS(w))
	uploadBtn := widget.NewButton("Upload File", ui.UploadFile(w))
	relocateBtn := widget.NewButton("Change Storage Path", changeVFSStorageLocation(a, w, ui.Refresh))

	tabVFS := container.NewBorder(
		container.NewHBox(syncBtn, uploadBtn, relocateBtn),
		nil, nil, nil,
		container.NewScroll(ui.vfsFilesContainer),
	)

	discoverServicesBtn := widget.NewButton("Discover Services", ui.DiscoverServices(w))

	tabServices := container.NewHSplit(
		container.NewBorder(discoverServicesBtn, nil, nil, nil, container.NewScroll(ui.servicesListContainer)),
		container.NewScroll(ui.serviceDetailContainer),
	)
	tabServices.SetOffset(0.4)

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", tabStatus),
		container.NewTabItem("Pairing", tabPairing),
		container.NewTabItem("VFS", tabVFS),
		container.NewTabItem("Services", tabServices),
	)

	w.SetContent(tabs)

	go func() {
		time.Sleep(2000 * time.Millisecond)
		appLogger = protocol.NewLogger(os.Stdout, false)
		prefPath := a.Preferences().StringWithFallback("app_storage_path", "")
		if prefPath != "" {
			appStorage = prefPath
		} else {
			dir := os.Getenv("FILESDIR")
			if dir == "" {
				s := a.Storage()
				if s != nil {
					if root := s.RootURI(); root != nil && root.Scheme() == "file" {
						dir = root.Path()
					}
				}
				if dir == "" {
					dir = "./data"
				}
			}
			appStorage = filepath.Join(dir, "proxyma_data")
		}
		_, err := protocol.LoadConfig(appStorage)
		if err != nil {
			if os.IsNotExist(err) {
				createInitialConfig(w, portEntry.Text)
			} else {
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
				return
			}
		}
		if err := startNode(); err != nil {
			fyne.Do(func() {
				dialog.ShowError(err, w)
			})
		}
		fyne.Do(ui.Refresh)
	}()

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		for range ticker.C {
			fyne.Do(ui.Refresh)
		}
	}()
	w.ShowAndRun()
	stopNode()
}

