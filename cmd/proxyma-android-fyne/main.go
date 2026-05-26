package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

var (
	srv        *server.Server
	srvTLS     *tls.Config
	srvMutex   sync.Mutex
	running    bool
	appStorage string
)

func main() {
	a := app.New()
	w := a.NewWindow("Proxyma Android")
	w.Resize(fyne.NewSize(450, 700))

	appStorage = filepath.Join(a.Storage().RootURI().Path(), "proxyma_data")
	_ = os.MkdirAll(appStorage, 0755)

	statusLabel := widget.NewLabel("Status: Offline")
	nodeIDLabel := widget.NewLabel("Node ID: -")
	nodeAddrLabel := widget.NewLabel("Address: -")
	peersContainer := container.NewVBox()

	var refreshUI func()

	inviteTokenEntry := widget.NewEntry()
	inviteTokenEntry.SetPlaceHolder("Generated smart token will appear here")
	generateInviteBtn := widget.NewButton("Generate Invite Token", func() {
		srvMutex.Lock()
		defer srvMutex.Unlock()
		if srv == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		smartToken, secretHex, err := p2p.GenerateSmartToken(srv.Config.Address, srv.Config.CAPath)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		expiration := time.Now().Add(15 * time.Minute)
		srv.Config.Logger.Info("Token generated in UI", "expires", expiration)
		srv.AddPendingInvite(secretHex, expiration)
		inviteTokenEntry.SetText(smartToken)
	})

	tokenEntry := widget.NewEntry()
	tokenEntry.SetPlaceHolder("Smart Token")
	nodeIDEntry := widget.NewEntry()
	nodeIDEntry.SetPlaceHolder("Node ID (optional)")
	portEntry := widget.NewEntry()
	portEntry.SetText("8080")

	joinBtn := widget.NewButton("Join Cluster", func() {
		go func() {
			token := tokenEntry.Text
			nid := nodeIDEntry.Text
			port := portEntry.Text
			if token == "" {
				dialog.ShowError(errors.New("Smart Token is required"), w)
				return
			}
			if nid == "" {
				hostname, err := os.Hostname()
				if err != nil {
					hostname = "node"
				}
				b := make([]byte, 2)
				_, _ = rand.Read(b)
				nid = fmt.Sprintf("%s-%s", hostname, hex.EncodeToString(b))
			}

			payload, secret, err := p2p.ParseSmartToken(token)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}

			csrPEM, privKeyPEM, err := p2p.GenerateNodeCSR(nid)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}

			tr := &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
					VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
						for _, rawCert := range rawCerts {
							hash := sha256.Sum256(rawCert)
							if hex.EncodeToString(hash[:]) == payload.CAHash {
								return nil
							}
						}
						return errors.New("security alert: identity mismatch")
					},
				},
			}
			client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

			localIP := getLocalIP()
			localAddr := fmt.Sprintf("https://%s:%s", localIP, port)

			reqBody := protocol.JoinRequest{
				Secret:  secret,
				CSR:     string(csrPEM),
				ID:      nid,
				Address: localAddr,
			}
			bodyBytes, _ := json.Marshal(reqBody)

			urlStr := fmt.Sprintf("%s/cluster/join", payload.Address)
			req, _ := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				dialog.ShowError(fmt.Errorf("cluster rejected join: status %d", resp.StatusCode), w)
				return
			}

			var joinResp protocol.JoinResponse
			if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
				dialog.ShowError(err, w)
				return
			}

			certsDir := filepath.Join(appStorage, "certs")
			_ = os.MkdirAll(certsDir, 0755)

			caPath := filepath.Join(certsDir, "ca.crt")
			certPath := filepath.Join(certsDir, fmt.Sprintf("%s.crt", nid))
			keyPath := filepath.Join(certsDir, fmt.Sprintf("%s.key", nid))

			_ = os.WriteFile(caPath, []byte(joinResp.CACert), 0644)
			_ = os.WriteFile(certPath, []byte(joinResp.Certificate), 0644)
			_ = os.WriteFile(keyPath, privKeyPEM, 0600)

			cfg := protocol.NodeConfig{
				ID:            nid,
				Address:       localAddr,
				StoragePath:   appStorage,
				Workers:       4,
				CAPath:        caPath,
				BootstrapNode: strings.Replace(payload.Address, "0.0.0.0", "node-1", 1),
			}
			_ = protocol.SaveConfig(cfg)

			dialog.ShowInformation("Success", "Joined cluster successfully", w)
			
			startNode()
			refreshUI()
		}()
	})

	statusCard := widget.NewCard("Node Status", "", container.NewVBox(
		statusLabel,
		nodeIDLabel,
		nodeAddrLabel,
	))

	inviteCard := widget.NewCard("Invite Peer", "", container.NewVBox(
		generateInviteBtn,
		inviteTokenEntry,
	))

	peersCard := widget.NewCard("Active Peers", "", peersContainer)

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

	vfsFilesContainer := container.NewVBox()

	syncBtn := widget.NewButton("Sync VFS", func() {
		srvMutex.Lock()
		s := srv
		srvMutex.Unlock()
		if s == nil {
			dialog.ShowError(errors.New("Node is not running"), w)
			return
		}
		go func() {
			_ = s.ExecuteSync()
			refreshUI()
		}()
	})

	uploadBtn := widget.NewButton("Upload File", func() {
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
				refreshUI()
			}
		}, w)
	})

	tabVFS := container.NewBorder(
		container.NewHBox(syncBtn, uploadBtn),
		nil, nil, nil,
		container.NewScroll(vfsFilesContainer),
	)

	servicesListContainer := container.NewVBox()
	serviceDetailContainer := container.NewVBox()

	discoverServicesBtn := widget.NewButton("Discover Services", func() {
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
			
			servicesListContainer.Objects = nil
			for name := range names {
				svcName := name
				btn := widget.NewButton(svcName, func() {
					loadServiceDetails(svcName, w, serviceDetailContainer)
				})
				servicesListContainer.Add(btn)
			}
			servicesListContainer.Refresh()
		}()
	})

	tabServices := container.NewHSplit(
		container.NewBorder(discoverServicesBtn, nil, nil, nil, container.NewScroll(servicesListContainer)),
		container.NewScroll(serviceDetailContainer),
	)
	tabServices.SetOffset(0.4)

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", tabStatus),
		container.NewTabItem("Pairing", tabPairing),
		container.NewTabItem("VFS", tabVFS),
		container.NewTabItem("Services", tabServices),
	)

	refreshUI = func() {
		srvMutex.Lock()
		s := srv
		isRun := running
		srvMutex.Unlock()

		if !isRun || s == nil {
			statusLabel.SetText("Status: Offline")
			nodeIDLabel.SetText("Node ID: -")
			nodeAddrLabel.SetText("Address: -")
			peersContainer.Objects = nil
			vfsFilesContainer.Objects = nil
			return
		}

		statusLabel.SetText("Status: Online")
		nodeIDLabel.SetText("Node ID: " + s.Config.ID)
		nodeAddrLabel.SetText("Address: " + s.Config.Address)

		peersContainer.Objects = nil
		for id, addr := range s.GetPeersCopy() {
			peersContainer.Add(widget.NewLabel(fmt.Sprintf("%s (%s)", id, addr)))
		}
		peersContainer.Refresh()

		vfsFilesContainer.Objects = nil
		for _, entry := range s.Storage.GetVFSSnapshot() {
			fileEntry := entry
			if fileEntry.Deleted {
				continue
			}

			hasLocal, _ := s.Storage.HasPhysicalBlob(fileEntry.Hash)
			statusText := "Remote"
			if hasLocal {
				statusText = "Local"
			}

			lbl := widget.NewLabel(fmt.Sprintf("%s (v%d, %s, %s)", fileEntry.Name, fileEntry.Version, byteCountSI(fileEntry.Size), statusText))

			var subBtn *widget.Button
			isSub := false
			s.Storage.GetVFSSnapshot() // trigger read check logic if needed
			
			subBtn = widget.NewButton("Subscribe", func() {
				s.Storage.SetSubscription(fileEntry.Name, true)
				refreshUI()
			})
			_ = isSub
			
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
					err = s.Storage.ReadPhysicalBlob(fileEntry.Hash, writer)
					if err != nil {
						dialog.ShowError(err, w)
					} else {
						dialog.ShowInformation("Exported", "File exported successfully", w)
					}
				}, w)
			})

			row := container.NewHBox(lbl, subBtn, exportBtn)
			vfsFilesContainer.Add(row)
		}
		vfsFilesContainer.Refresh()
	}

	w.SetContent(tabs)

	startNode()
	refreshUI()

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		for range ticker.C {
			refreshUI()
		}
	}()

	w.ShowAndRun()
	stopNode()
}

func startNode() {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if running {
		return
	}

	cfg, err := protocol.LoadConfig(appStorage)
	if err != nil {
		return
	}

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
	nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return
	}

	srvTLS = stls
	baseTransport := &http.Transport{TLSClientConfig: ctls}
	peerClient := p2p.NewHTTPPeerClient(baseTransport, cfg.BootstrapNode, slog.Default())

	srv = server.New(cfg, peerClient)
	srv.LoadLocalServices()

	go func() {
		_ = srv.ListenAndServe(srvTLS)
	}()

	if cfg.BootstrapNode != "" {
		go func() {
			time.Sleep(2 * time.Second)
			_ = srv.AnnouncePresence(cfg.BootstrapNode)
			if srv != nil {
				go srv.StartRelayPolling(context.Background(), cfg.BootstrapNode)
			}
		}()
	}

	running = true
}

func stopNode() {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if !running || srv == nil {
		return
	}
	_ = srv.Shutdown(context.Background())
	srv = nil
	running = false
}

func loadServiceDetails(name string, w fyne.Window, dest *fyne.Container) {
	srvMutex.Lock()
	s := srv
	srvMutex.Unlock()
	if s == nil {
		return
	}

	go func() {
		addr, schema, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
		if err != nil {
			dialog.ShowError(err, w)
			return
		}

		dest.Objects = nil
		dest.Add(widget.NewLabel("Service: " + schema.Name))
		dest.Add(widget.NewLabel("Description: " + schema.Description))
		dest.Add(widget.NewLabel("Provider Address: " + addr))

		inputs := make(map[string]any)

		for pName, pRules := range schema.Parameters {
			paramName := pName
			rules := pRules
			dest.Add(widget.NewLabel(paramName + " (" + rules.Type + ", Required: " + strconv.FormatBool(rules.Required) + ")"))

			if rules.Type == "bool" {
				chk := widget.NewCheck("", func(val bool) {
					inputs[paramName] = val
				})
				dest.Add(chk)
			} else if rules.Type == "int" {
				entry := widget.NewEntry()
				entry.OnChanged = func(val string) {
					v, _ := strconv.Atoi(val)
					inputs[paramName] = v
				}
				dest.Add(entry)
			} else if rules.Type == "float" {
				entry := widget.NewEntry()
				entry.OnChanged = func(val string) {
					v, _ := strconv.ParseFloat(val, 64)
					inputs[paramName] = v
				}
				dest.Add(entry)
			} else {
				if strings.Contains(strings.ToLower(paramName), "image") || strings.Contains(strings.ToLower(paramName), "img") {
					btnContainer := container.NewVBox()
					valLabel := widget.NewLabel("No image selected")
					
					chooseBtn := widget.NewButton("Choose Image (Photo/Gallery)", func() {
						dialog.ShowCustomConfirm("Select Image Source", "Take Photo", "Open Gallery", widget.NewLabel("Choose an option"), func(take bool) {
							if take {
								tempPath := filepath.Join(appStorage, fmt.Sprintf("photo_%d.jpg", time.Now().Unix()))
								err := capturePhoto(tempPath)
								if err != nil {
									dialog.ShowError(err, w)
									return
								}
								dialog.ShowCustomConfirm("Camera Invoked", "Proceed", "Cancel", widget.NewLabel("Press Proceed once the photo is captured"), func(proceed bool) {
									if proceed {
										f, err := os.Open(tempPath)
										if err != nil {
											dialog.ShowError(err, w)
											return
										}
										defer f.Close()
										vfsName := filepath.Base(tempPath)
										err = s.Storage.SaveLocalFile(vfsName, f)
										if err != nil {
											dialog.ShowError(err, w)
											return
										}
										inputs[paramName] = vfsName
										valLabel.SetText(vfsName)
									}
								}, w)
							} else {
								dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
									if err != nil || reader == nil {
										return
									}
									defer reader.Close()
									vfsName := reader.URI().Name()
									err = s.Storage.SaveLocalFile(vfsName, reader)
									if err != nil {
										dialog.ShowError(err, w)
										return
									}
									inputs[paramName] = vfsName
									valLabel.SetText(vfsName)
								}, w)
							}
						}, w)
					})
					btnContainer.Add(chooseBtn)
					btnContainer.Add(valLabel)
					dest.Add(btnContainer)
				} else {
					entry := widget.NewEntry()
					entry.OnChanged = func(val string) {
						inputs[paramName] = val
					}
					dest.Add(entry)
				}
			}
		}

		runBtn := widget.NewButton("Run Service", func() {
			go func() {
				taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())
				
				var targetPeerID string
				for pid, paddr := range s.GetPeersCopy() {
					if paddr == addr {
						targetPeerID = pid
						break
					}
				}
				if targetPeerID == "" {
					targetPeerID = addr
				}

				payloadMap := make(map[string]any)
				for k, v := range inputs {
					payloadMap[k] = v
				}

				req := protocol.TaskRequest{
					TaskID:  taskID,
					Service: schema.Name,
					ReplyTo: fmt.Sprintf("%s/services/callback", s.Config.Address),
					Payload: payloadMap,
				}

				err = s.DispatchTask(targetPeerID, req)
				if err != nil {
					dialog.ShowError(err, w)
					return
				}

				progress := dialog.NewProgress("Running Service", "Executing compute task...", w)
				progress.Show()

				var resp protocol.ServiceTaskResponse
				completed := false
				for i := 0; i < 30; i++ {
					time.Sleep(1 * time.Second)
					r, ok := s.Compute.GetTaskResponse(taskID)
					if ok {
						if r.Status == "completed" || r.Status == "failed" {
							resp = r
							completed = true
							break
						}
					}
				}
				progress.Hide()

				if !completed {
					dialog.ShowError(errors.New("Task execution timed out"), w)
					return
				}

				if resp.Status == "failed" {
					dialog.ShowError(errors.New(resp.Error), w)
				} else {
					outBytes, _ := json.MarshalIndent(resp.Outputs, "", "  ")
					dialog.ShowInformation("Execution Complete", string(outBytes), w)
				}
			}()
		})
		dest.Add(runBtn)
		dest.Refresh()
	}()
}

func capturePhoto(tempPath string) error {
	if runtime.GOOS == "android" {
		cmd := exec.Command("am", "start", "-a", "android.media.action.IMAGE_CAPTURE", "--eu", "output", "file://"+tempPath)
		return cmd.Run()
	}
	f, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	_, _ = f.Write([]byte("mock jpeg camera output data"))
	_ = f.Close()
	return nil
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func byteCountSI(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}
