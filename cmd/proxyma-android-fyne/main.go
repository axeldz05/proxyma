package main

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	appCtx     context.Context
)

type LogRecord struct {
	Timestamp time.Time
	Level     string // "INFO", "WARN", "ERROR", "DEBUG"
	Message   string
}

var (
	logBuffer   []LogRecord
	logBufferMu sync.Mutex
	logUIUpdate func()
)

type LogWriter struct {
	Stdout io.Writer
}

func (w *LogWriter) Write(p []byte) (n int, err error) {
	n, err = w.Stdout.Write(p)
	line := string(p)
	level := "INFO"
	if strings.Contains(line, "level=ERROR") || strings.Contains(line, "level=error") {
		level = "ERROR"
	} else if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=warn") {
		level = "WARN"
	} else if strings.Contains(line, "level=DEBUG") || strings.Contains(line, "level=debug") {
		level = "DEBUG"
	}

	logBufferMu.Lock()
	logBuffer = append(logBuffer, LogRecord{
		Timestamp: time.Now(),
		Level:     level,
		Message:   strings.TrimSpace(line),
	})
	if len(logBuffer) > 500 {
		logBuffer = logBuffer[len(logBuffer)-500:]
	}
	logBufferMu.Unlock()

	if logUIUpdate != nil {
		logUIUpdate()
	}
	return n, err
}

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
		showInfo:               true,
		showWarn:               true,
		showError:              true,
		logsContainer:          container.NewVBox(),
	}

	logUIUpdate = func() {
		fyne.Do(ui.refreshLogs)
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
	joinBtn := widget.NewButton("Join Cluster", joinCluster(tokenEntry, w, nodeIDEntry, portEntry, ui.Refresh))

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

	infoCheck := widget.NewCheck("Info", func(val bool) {
		ui.showInfo = val
		ui.refreshLogs()
	})
	infoCheck.SetChecked(true)

	warnCheck := widget.NewCheck("Warning", func(val bool) {
		ui.showWarn = val
		ui.refreshLogs()
	})
	warnCheck.SetChecked(true)

	errorCheck := widget.NewCheck("Error", func(val bool) {
		ui.showError = val
		ui.refreshLogs()
	})
	errorCheck.SetChecked(true)

	logToggles := container.NewHBox(infoCheck, warnCheck, errorCheck)
	scrollLogs := container.NewScroll(ui.logsContainer)
	tabLogs := container.NewBorder(logToggles, nil, nil, nil, scrollLogs)

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", tabStatus),
		container.NewTabItem("Pairing", tabPairing),
		container.NewTabItem("VFS", tabVFS),
		container.NewTabItem("Services", tabServices),
		container.NewTabItem("Logs", tabLogs),
	)

	w.SetContent(tabs)

	ctx, cancel := context.WithCancel(context.Background())
	appCtx = ctx

	go func() {
		select {
		case <-time.After(2000 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		writer := &LogWriter{Stdout: os.Stdout}
		appLogger = protocol.NewLogger(writer, true)
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
				select {
				case <-ctx.Done():
					return
				default:
				}
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := startNode(); err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			fyne.Do(func() {
				dialog.ShowError(err, w)
			})
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		fyne.Do(ui.Refresh)
	}()

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fyne.Do(ui.Refresh)
			}
		}
	}()

	w.SetCloseIntercept(func() {
		cancel()
		stopNode()
		w.Close()
	})

	w.ShowAndRun()
	cancel()
	stopNode()
}
