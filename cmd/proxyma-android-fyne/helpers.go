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
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func generateInviteToken(w fyne.Window, inviteTokenEntry *widget.Entry) func() {
	return func() {
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
	}
}

func joinCluster(token string, w fyne.Window, nid string, port string, refreshUI func()) func() {
	return func() {
		go func() {
			cfg := loadConfigOrDie(appStorage, w)
			if token == "" {
				dialog.ShowError(errors.New("Smart Token is required"), w)
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
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
				return
			}

			csrPEM, privKeyPEM, err := p2p.GenerateNodeCSR(nid)
			if err != nil {
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
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
			client := &http.Client{Transport: tr}

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
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("cluster rejected join: status %d", resp.StatusCode), w)
				})
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("cluster rejected join: status %d", resp.StatusCode), w)
				})
				return
			}

			var joinResp protocol.JoinResponse
			if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("cluster rejected join: status %d", resp.StatusCode), w)
				})
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

			newCfg := protocol.NodeConfig{
				ID:            nid,
				Address:       localAddr,
				StoragePath:   appStorage,
				Workers:       cfg.Workers,
				CAPath:        caPath,
				BootstrapNode: strings.Replace(payload.Address, "0.0.0.0", "node-1", 1),
			}
			err = protocol.SaveConfig(newCfg)
			if err != nil {
				fyne.Do(func() {
					dialog.ShowError(err, w)
				})
			}
			fyne.Do(func() {
				dialog.ShowInformation("Success", "Joined cluster successfully", w)
				startNode()
				refreshUI()
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

	certsDir := filepath.Dir(cfg.CAPath)
	nodeCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", cfg.ID))
	nodeKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", cfg.ID))

	stls, ctls, err := p2p.LoadNodeTLS(cfg.CAPath, nodeCertFile, nodeKeyFile)
	if err != nil {
		return err
	}

	srvTLS = stls
	baseTransport := &http.Transport{TLSClientConfig: ctls}
	peerClient := p2p.NewHTTPPeerClient(baseTransport, cfg.BootstrapNode, slog.Default())

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
			go srv.StartRelayPolling(context.Background(), cfg.BootstrapNode)
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
	_ = srv.Shutdown(context.Background())
	srv = nil
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
			fyne.Do(func() {
				dialog.ShowError(err, w)
			})
			return
		}

		inputs := make(map[string]any)

		fyne.Do(func() {
			dest.Objects = nil
			dest.Add(widget.NewLabel("Service: " + schema.Name))
			dest.Add(widget.NewLabel("Description: " + schema.Description))
			dest.Add(widget.NewLabel("Provider Address: " + addr))

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

func createInitialConfig(w fyne.Window, port string) {
	_ = os.MkdirAll(appStorage, 0755)
	certsDir := filepath.Join(appStorage, "certs")
	_ = os.MkdirAll(certsDir, 0755)

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "node"
	}
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	nid := fmt.Sprintf("%s-%s", hostname, hex.EncodeToString(b))
	if err := p2p.InitCluster(certsDir); err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return
	}
	if err := p2p.IssueNodeCertificate(certsDir, certsDir, nid); err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return
	}

	localIP := getLocalIP()
	localAddr := fmt.Sprintf("https://%s:%s", localIP, port)
	caPath := filepath.Join(certsDir, "ca.crt")
	cfg := protocol.NodeConfig{
		ID:          nid,
		Address:     localAddr,
		StoragePath: appStorage,
		Workers:     4,
		CAPath:      caPath,
	}
	if err = protocol.SaveConfig(cfg); err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return
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
