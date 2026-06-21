package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/utils"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func getRunningServer() *server.Server {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	return srv
}

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

func startNode() error {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	cfg, err := protocol.LoadConfig(appStorage)
	if err != nil {
		return err
	}
	cfg.Logger = appLogger

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
	nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return err
	}

	srvTLS = stls
	baseTransport := &http.Transport{TLSClientConfig: ctls}
	wrappedTransport := &bandwidthRoundTripper{base: baseTransport}
	peerClient := p2p.NewHTTPPeerClient(wrappedTransport, cfg.BootstrapNode, appLogger)

	srv = server.New(cfg, peerClient)
	srv.LoadLocalServices()

	go func() error {
		err = srv.ListenAndServe(srvTLS)
		if err != nil {
			return err
		}
		return nil
	}()

	if cfg.BootstrapNode != "" {
		go func() error {
			time.Sleep(2 * time.Second)
			err = srv.AnnouncePresence(cfg.BootstrapNode)
			if err != nil {
				return err
			}
			go srv.StartRelayPolling(appCtx, cfg.BootstrapNode)
			return nil
		}()
	}

	return nil
}

func stopNode() {
	srvMutex.Lock()
	defer srvMutex.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	srv = nil
}

func loadServiceDetails(name string, w fyne.Window, dest *fyne.Container) {
	s := getRunningServer()
	if s == nil {
		return
	}

	go func() {
		addr, schema, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
		if err != nil {
			fyne.Do(func() {
				dialog.ShowError(err, w)
			})
			return
		}

		inputs := make(map[string]any)

		fyne.Do(func() {
			dest.Objects = []fyne.CanvasObject{}
			dest.Add(widget.NewLabel("Service: " + schema.Name))
			dest.Add(widget.NewLabel("Description: " + schema.Description))
			dest.Add(widget.NewLabel("Provider Address: " + addr))

			var reqPermissions []string
			hasImageParam := false
			hasFileParam := false

			for pName := range schema.Parameters {
				lowerName := strings.ToLower(pName)
				if strings.Contains(lowerName, "image") || strings.Contains(lowerName, "img") || strings.Contains(lowerName, "photo") {
					hasImageParam = true
				}
				if strings.Contains(lowerName, "file") || strings.Contains(lowerName, "path") {
					hasFileParam = true
				}
			}

			if hasImageParam {
				reqPermissions = append(reqPermissions, "Camera (to take photo for upload)")
				reqPermissions = append(reqPermissions, "Gallery / Storage (to select photo)")
			} else if hasFileParam {
				reqPermissions = append(reqPermissions, "Storage (to read/write local files)")
			}

			if len(reqPermissions) > 0 {
				dest.Add(widget.NewLabel("Required Permissions:"))
				for _, perm := range reqPermissions {
					dest.Add(widget.NewLabel(" - " + perm))
				}
			} else {
				dest.Add(widget.NewLabel("Required Permissions: None"))
			}

			for paramName, rules := range schema.Parameters {
				for _, obj := range buildParameterWidget(paramName, rules, inputs, w, s) {
					dest.Add(obj)
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
						fyne.Do(func() {
							dialog.ShowError(err, w)
						})
						return
					}

					var progress *dialog.ProgressDialog
					fyne.Do(func() {
						progress = dialog.NewProgress("Running Service", "Executing compute task...", w)
						progress.Show()
					})

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

					fyne.Do(func() {
						if progress != nil {
							progress.Hide()
						}
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
					})
				}()
			})
			dest.Add(runBtn)
			dest.Refresh()
		})
	}()
}

func buildParameterWidget(paramName string, rules protocol.ServiceParameter, inputs map[string]any, w fyne.Window, s *server.Server) []fyne.CanvasObject {
	var objects []fyne.CanvasObject
	objects = append(objects, widget.NewLabel(paramName+" ("+rules.Type+", Required: "+strconv.FormatBool(rules.Required)+")"))

	descLabel := ""
	if rules.Type == "bool" {
		descLabel = fmt.Sprintf("Description: Toggle to enable or disable the %s option.", paramName)
	} else if rules.Type == "int" || rules.Type == "float" {
		descLabel = fmt.Sprintf("Description: Enter a numerical value for %s.", paramName)
	} else {
		if strings.Contains(strings.ToLower(paramName), "image") || strings.Contains(strings.ToLower(paramName), "img") {
			descLabel = fmt.Sprintf("Description: Provide an image file path or capture a photo for %s.", paramName)
		} else {
			descLabel = fmt.Sprintf("Description: Provide a text value for %s.", paramName)
		}
	}
	objects = append(objects, widget.NewLabel(descLabel))

	if rules.Type == "bool" {
		chk := widget.NewCheck("", func(val bool) {
			inputs[paramName] = val
		})
		objects = append(objects, chk)
	} else if rules.Type == "int" {
		entry := widget.NewEntry()
		entry.OnChanged = func(val string) {
			v, _ := strconv.Atoi(val)
			inputs[paramName] = v
		}
		objects = append(objects, entry)
	} else if rules.Type == "float" {
		entry := widget.NewEntry()
		entry.OnChanged = func(val string) {
			v, _ := strconv.ParseFloat(val, 64)
			inputs[paramName] = v
		}
		objects = append(objects, entry)
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
								saveReaderToVFS(w, s, vfsName, f, func() {
									inputs[paramName] = vfsName
									valLabel.SetText(vfsName)
								})
							}
						}, w)
					} else {
						dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
							if err != nil || reader == nil {
								return
							}
							defer reader.Close()
							vfsName := reader.URI().Name()
							saveReaderToVFS(w, s, vfsName, reader, func() {
								inputs[paramName] = vfsName
								valLabel.SetText(vfsName)
							})
						}, w)
					}
				}, w)
			})
			btnContainer.Add(chooseBtn)
			btnContainer.Add(valLabel)
			objects = append(objects, btnContainer)
		} else {
			entry := widget.NewEntry()
			entry.OnChanged = func(val string) {
				inputs[paramName] = val
			}
			objects = append(objects, entry)
		}
	}
	return objects
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
	ips, _ := utils.GetLocalIPs()
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src string, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	err = os.MkdirAll(dst, srcInfo.Mode())
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			err = copyDir(srcPath, dstPath)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(srcPath, dstPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
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

type bandwidthRoundTripper struct {
	base http.RoundTripper
}

func (b *bandwidthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body = &utils.CountingReadCloser{
			ReadCloser: req.Body,
			OnRead: func(n int) {
				if s := getRunningServer(); s != nil {
					s.RecordBytesSent(int64(n), req.URL.RequestURI())
				}
			},
		}
	}

	resp, err := b.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil {
		resp.Body = &utils.CountingReadCloser{
			ReadCloser: resp.Body,
			OnRead: func(n int) {
				if s := getRunningServer(); s != nil {
					s.RecordBytesReceived(int64(n), req.URL.RequestURI())
				}
			},
		}
	}
	return resp, nil
}
