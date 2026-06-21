package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

func buildServicePermissionsLabels(schema protocol.ServiceSchema) []string {
	var reqPermissions []string
	hasImageParam := false
	hasFileParam := false

	for pName := range schema.Parameters {
		if isImageParam(pName) {
			hasImageParam = true
		}
		if isFileParam(pName) {
			hasFileParam = true
		}
	}

	if hasImageParam {
		reqPermissions = append(reqPermissions, "Camera (to take photo for upload)")
		reqPermissions = append(reqPermissions, "Gallery / Storage (to select photo)")
	} else if hasFileParam {
		reqPermissions = append(reqPermissions, "Storage (to read/write local files)")
	}

	return reqPermissions
}

func runServiceTask(w fyne.Window, s *server.Server, schema protocol.ServiceSchema, providerAddr string, inputs map[string]any) {
	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())

	var targetPeerID string
	for pid, paddr := range s.GetPeersCopy() {
		if paddr == providerAddr {
			targetPeerID = pid
			break
		}
	}
	if targetPeerID == "" {
		targetPeerID = providerAddr
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

	err := s.DispatchTask(targetPeerID, req)
	if err != nil {
		fyne.Do(func() {
			dialog.ShowError(err, w)
		})
		return
	}

	var progress dialog.Dialog
	fyne.Do(func() {
		bar := widget.NewProgressBarInfinite()
		progress = dialog.NewCustomWithoutButtons("Running Service", container.NewVBox(
			widget.NewLabel("Executing compute task..."),
			bar,
		), w)
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
			dialog.ShowError(uiError("Task execution timed out"), w)
			return
		}
		if resp.Status == "failed" {
			dialog.ShowError(errors.New(resp.Error), w)
		} else {
			outBytes, _ := json.MarshalIndent(resp.Outputs, "", "  ")
			dialog.ShowInformation("Execution Complete", string(outBytes), w)
		}
	})
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

			reqPermissions := buildServicePermissionsLabels(schema)
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
				go runServiceTask(w, s, schema, addr, inputs)
			})
			dest.Add(runBtn)
			dest.Refresh()
		})
	}()
}

func isImageParam(paramName string) bool {
	lower := strings.ToLower(paramName)
	return strings.Contains(lower, "image") || strings.Contains(lower, "img") || strings.Contains(lower, "photo")
}

func isFileParam(paramName string) bool {
	lower := strings.ToLower(paramName)
	return strings.Contains(lower, "file") || strings.Contains(lower, "path")
}

func getParamDescription(paramName, paramType string) string {
	switch paramType {
	case "bool":
		return fmt.Sprintf("Description: Toggle to enable or disable the %s option.", paramName)
	case "int", "float":
		return fmt.Sprintf("Description: Enter a numerical value for %s.", paramName)
	default:
		if isImageParam(paramName) {
			return fmt.Sprintf("Description: Provide an image file path or capture a photo for %s.", paramName)
		}
		return fmt.Sprintf("Description: Provide a text value for %s.", paramName)
	}
}

func buildImagePickerWidget(paramName string, inputs map[string]any, w fyne.Window, s *server.Server) fyne.CanvasObject {
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
						defer func() { _ = f.Close() }()
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
					defer func() { _ = reader.Close() }()
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
	return btnContainer
}

func buildParameterWidget(paramName string, rules protocol.ServiceParameter, inputs map[string]any, w fyne.Window, s *server.Server) []fyne.CanvasObject {
	var objects []fyne.CanvasObject
	objects = append(objects, widget.NewLabel(paramName+" ("+rules.Type+", Required: "+strconv.FormatBool(rules.Required)+")"))

	objects = append(objects, widget.NewLabel(getParamDescription(paramName, rules.Type)))

	var inputWidget fyne.CanvasObject
	switch rules.Type {
	case "bool":
		inputWidget = widget.NewCheck("", func(val bool) {
			inputs[paramName] = val
		})
	case "int":
		entry := widget.NewEntry()
		entry.OnChanged = func(val string) {
			v, _ := strconv.Atoi(val)
			inputs[paramName] = v
		}
		inputWidget = entry
	case "float":
		entry := widget.NewEntry()
		entry.OnChanged = func(val string) {
			v, _ := strconv.ParseFloat(val, 64)
			inputs[paramName] = v
		}
		inputWidget = entry
	default:
		if isImageParam(paramName) {
			inputWidget = buildImagePickerWidget(paramName, inputs, w, s)
		} else {
			entry := widget.NewEntry()
			entry.OnChanged = func(val string) {
				inputs[paramName] = val
			}
			inputWidget = entry
		}
	}

	objects = append(objects, inputWidget)
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
