package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"
	"proxyma/shared/uischema"

	"github.com/spf13/cobra"
)

var (
	cliStorage string
	rootCmd    = &cobra.Command{
		Use:   "proxyma",
		Short: "Proxyma is a distributed P2P compute and storage engine",
		Long: `A secure and distributed P2P cluster.
Allows synchronizing files and running compute tasks between nodes encrypted with mutual TLS.`,
	}
)

type BandwidthStats struct {
	DownloadSpeed int64 `json:"downloadSpeed"`
	UploadSpeed   int64 `json:"uploadSpeed"`
	TotalReceived int64 `json:"totalReceived"`
	TotalSent     int64 `json:"totalSent"`
}

func formatBytes(bytesVal int64) string {
	if bytesVal <= 0 {
		return "0 B"
	}
	if bytesVal >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytesVal)/(1024*1024*1024))
	} else if bytesVal >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytesVal)/(1024*1024))
	} else if bytesVal >= 1024 {
		return fmt.Sprintf("%.2f KB", float64(bytesVal)/1024)
	}
	return fmt.Sprintf("%d B", bytesVal)
}

func init() {
	defaultStorage := getDefaultStorage()
	rootCmd.PersistentFlags().StringVar(&cliStorage, "storage", defaultStorage, "Path to the local node's directory")

	// Dynamically register Cobra commands from SSOT Registry
	for _, domain := range uischema.Registry {
		domainCopy := domain
		domainCmd := &cobra.Command{
			Use:   domainCopy.Name,
			Short: domainCopy.Title,
		}

		for _, action := range domainCopy.Actions {
			actionCopy := action
			actionCmd := &cobra.Command{
				Use:   actionCopy.Name,
				Short: actionCopy.Description,
			}

			// Add flags for parameters
			for _, param := range actionCopy.Parameters {
				paramCopy := param
				switch paramCopy.Type {
				case "bool":
					defaultVal := paramCopy.DefaultValue == "true" || paramCopy.DefaultValue == "1"
					actionCmd.Flags().Bool(paramCopy.Name, defaultVal, paramCopy.Description)
				case "int":
					var defaultVal int
					if paramCopy.DefaultValue != "" {
						_, _ = fmt.Sscanf(paramCopy.DefaultValue, "%d", &defaultVal)
					}
					actionCmd.Flags().Int(paramCopy.Name, defaultVal, paramCopy.Description)
				default:
					actionCmd.Flags().String(paramCopy.Name, paramCopy.DefaultValue, paramCopy.Description)
				}
				if paramCopy.Required {
					_ = actionCmd.MarkFlagRequired(paramCopy.Name)
				}
			}

			if actionCopy.Domain == "service" && (actionCopy.Name == "run" || actionCopy.Name == "stream" || actionCopy.Name == "run_file") {
				origHelpFunc := actionCmd.HelpFunc()
				actionCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
					svcName, _ := c.Flags().GetString("name")
					if svcName == "" {
						svcName, _ = c.Flags().GetString("service")
					}
					if svcName != "" {
						payloadVal, _ := c.Flags().GetString("inputs")
						if payloadVal == "" {
							payloadVal, _ = c.Flags().GetString("payload")
						}
						if payloadVal == "" {
							payloadVal, _ = c.Flags().GetString("param")
						}
						handled, _ := ValidateAndPrintServiceHelp(cliStorage, svcName, payloadVal, actionCopy.Name, true)
						if handled {
							return
						}
					}
					origHelpFunc(c, args)
				})
			}

			actionCmd.RunE = func(cmd *cobra.Command, args []string) error {
				_ = loadConfigOrDie(cliStorage)
				proxyma_bind.SetStoragePath(cliStorage)

				argsMap := make(map[string]string)
				for _, param := range actionCopy.Parameters {
					switch param.Type {
					case "bool":
						val, _ := cmd.Flags().GetBool(param.Name)
						argsMap[param.Name] = fmt.Sprintf("%t", val)
					case "int":
						val, _ := cmd.Flags().GetInt(param.Name)
						argsMap[param.Name] = fmt.Sprintf("%d", val)
					default:
						val, _ := cmd.Flags().GetString(param.Name)
						argsMap[param.Name] = val
					}
				}

				if actionCopy.Domain == "service" && (actionCopy.Name == "run" || actionCopy.Name == "stream" || actionCopy.Name == "run_file") {
					svcName := argsMap["name"]
					if svcName == "" {
						svcName = argsMap["service"]
					}
					payloadRaw := argsMap["inputs"]
					if payloadRaw == "" {
						payloadRaw = argsMap["payload"]
					}
					if payloadRaw == "" {
						payloadRaw = argsMap["param"]
					}

					handled, err := ValidateAndPrintServiceHelp(cliStorage, svcName, payloadRaw, actionCopy.Name, false)
					if handled && err != nil {
						return err
					}
				}

				resJSON := executeActionLocal(actionCopy.Domain, actionCopy.Name, argsMap)

				// Check for errors in response
				if strings.Contains(resJSON, `"error":`) {
					var errResp struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal([]byte(resJSON), &errResp); err == nil && errResp.Error != "" {
						schemaFile := argsMap["schema-file"]
						if schemaFile == "" && (strings.HasSuffix(argsMap["name"], ".json") || fileExists(argsMap["name"])) {
							schemaFile = argsMap["name"]
						}
						if schemaFile != "" {
							fmt.Printf("❌ Failed to add pipeline schema from file '%s': %s\n", schemaFile, errResp.Error)
							fmt.Printf("💡 Tip: You can open and edit this schema file in the visual editor by running:\n")
							fmt.Printf("   proxyma service edit_pipeline --file %s\n\n", schemaFile)

							if isTerminalInteractive() {
								fmt.Print("Would you like to open this file in the visual pipeline editor now? [y/N]: ")
								var answer string
								_, _ = fmt.Scanln(&answer)
								answer = strings.ToLower(strings.TrimSpace(answer))
								if answer == "y" || answer == "yes" {
									resJSON = launchEditor("", schemaFile)
									if !strings.Contains(resJSON, `"error":`) {
										return nil
									}
								}
							}
						}
						return fmt.Errorf("%s", errResp.Error)
					}
				}

				// Render success output based on OutputType
				switch actionCopy.OutputType {
				case "table":
					var list []map[string]any
					if err := json.Unmarshal([]byte(resJSON), &list); err != nil {
						// Fallback: simple string list (like service/discover)
						var strList []string
						if err2 := json.Unmarshal([]byte(resJSON), &strList); err2 == nil {
							for _, val := range strList {
								fmt.Printf(" - %s\n", val)
							}
							return nil
						}
						return fmt.Errorf("failed to parse table data: %w", err)
					}

					if len(list) == 0 {
						fmt.Println("No records found.")
						return nil
					}

					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					var headers []string
					for _, col := range actionCopy.Columns {
						headers = append(headers, col.Header)
					}
					_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))

					for _, item := range list {
						var rowFields []string
						for _, col := range actionCopy.Columns {
							var formatted string
							if col.FieldSelector == "." {
								// Whole item string fallback
								formatted = fmt.Sprintf("%v", item)
							} else {
								val := item[col.FieldSelector]
								if val == nil {
									val = ""
								}
								if slice, ok := val.([]any); ok {
									formatted = fmt.Sprintf("%d", len(slice))
								} else {
									formatted = fmt.Sprintf("%v", val)
								}

								switch col.Format {
								case "bytes":
									var bytesVal int64
									if fv, ok := val.(float64); ok {
										bytesVal = int64(fv)
									} else if iv, ok := val.(int64); ok {
										bytesVal = iv
									}
									formatted = formatBytes(bytesVal)
								case "boolean":
									if bv, ok := val.(bool); ok {
										formatted = fmt.Sprintf("%t", bv)
									}
								case "status":
									switch col.FieldSelector {
									case "deleted":
										if bv, ok := val.(bool); ok && bv {
											formatted = "Deleted"
										} else {
											formatted = "Active"
										}
									case "online":
										if bv, ok := val.(bool); ok && bv {
											formatted = "ONLINE"
										} else {
											formatted = "OFFLINE"
										}
									}
								}
							}
							rowFields = append(rowFields, formatted)
						}
						_, _ = fmt.Fprintln(w, strings.Join(rowFields, "\t"))
					}
					_ = w.Flush()

				case "json":
					var indented bytes.Buffer
					if err := json.Indent(&indented, []byte(resJSON), "", "  "); err == nil {
						fmt.Println(indented.String())
					} else {
						fmt.Println(resJSON)
					}

				case "text":
					var msgResp struct {
						Message string `json:"message"`
					}
					if err := json.Unmarshal([]byte(resJSON), &msgResp); err == nil && msgResp.Message != "" {
						fmt.Println(msgResp.Message)
					} else {
						fmt.Println(resJSON)
					}
				}

				return nil
			}

			domainCmd.AddCommand(actionCmd)
		}

		rootCmd.AddCommand(domainCmd)
	}
}

func executeActionLocal(domain string, action string, args map[string]string) string {
	retErr := func(err string) string {
		return fmt.Sprintf(`{"error": %q}`, err)
	}
	retMsg := func(msg string) string {
		return fmt.Sprintf(`{"message": %q}`, msg)
	}
	runAction := func(fn func() string, successMsg string) string {
		errStr := fn()
		if errStr != "" {
			return retErr(errStr)
		}
		return retMsg(successMsg)
	}

	switch domain {
	case "storage":
		switch action {
		case "list":
			return proxyma_bind.GetVFSFilesJson()
		case "upload":
			filePath := args["path"]
			name := args["name"]
			if name == "" {
				name = filepath.Base(filePath)
			}
			return runAction(func() string { return proxyma_bind.UploadFile(name, filePath) }, fmt.Sprintf("File '%s' uploaded successfully to VFS.", name))
		case "subscribe":
			name := args["name"]
			return runAction(func() string { return proxyma_bind.SetSubscription(name, true) }, fmt.Sprintf("Subscribed to file '%s'. Synchronization triggered.", name))
		case "unsubscribe":
			name := args["name"]
			return runAction(func() string { return proxyma_bind.SetSubscription(name, false) }, fmt.Sprintf("Unsubscribed from file '%s'.", name))
		case "delete":
			name := args["name"]
			return runAction(func() string { return proxyma_bind.DeleteFile(name) }, fmt.Sprintf("File '%s' marked as deleted in VFS registry.", name))
		case "purge":
			name := args["name"]
			return runAction(func() string { return proxyma_bind.DeleteLocalCache(name) }, fmt.Sprintf("Physical cache for file '%s' purged from disk.", name))
		case "open":
			name := args["name"]
			if name == "" {
				return retErr("missing name parameter")
			}
			errStr := proxyma_bind.FetchFileOnDemand(name)
			if errStr != "" {
				return retErr(fmt.Sprintf("failed to fetch file '%s' on demand: %s", name, errStr))
			}
			filesJson := proxyma_bind.GetVFSFilesJson()
			var files []protocol.VFSFileStatus
			_ = json.Unmarshal([]byte(filesJson), &files)
			var hash string
			for _, f := range files {
				if f.Name == name {
					hash = f.Hash
					break
				}
			}
			if hash == "" {
				return retErr(fmt.Sprintf("file '%s' not found in VFS topology", name))
			}
			localPath := proxyma_bind.GetLocalBlobPath(hash)
			openedPath, err := openFileWithSystemDefault(cliStorage, name, localPath)
			if err != nil {
				return retErr(fmt.Sprintf("File '%s' fetched into cache at %s, but failed to launch default app: %v", name, localPath, err))
			}
			return retMsg(fmt.Sprintf("File '%s' fetched on-demand into cache and opened with system app at: %s", name, openedPath))
		case "sync":
			return runAction(proxyma_bind.SyncVFS, "Synchronization triggered successfully.")
		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	case "peers":
		switch action {
		case "list":
			return proxyma_bind.GetPeersJson()
		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	case "cluster":
		switch action {
		case "invite":
			token := proxyma_bind.GenerateInviteToken()
			if strings.HasPrefix(token, "error:") {
				return retErr(strings.TrimPrefix(token, "error: "))
			}
			return retMsg(token)
		case "join":
			token := args["token"]
			port := args["port"]
			if port == "" {
				port = "8080"
			}
			nodeID := args["node_id"]
			errStr := proxyma_bind.JoinCluster(cliStorage, token, nodeID, port)
			if errStr != "" {
				return retErr(errStr)
			}
			return retMsg("Joined cluster successfully!")
		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	case "telemetry":
		switch action {
		case "logs":
			return proxyma_bind.GetLogsJson()
		case "stats":
			statsJson := proxyma_bind.GetBandwidthStatsJson()
			if strings.Contains(statsJson, `"error":`) {
				return statsJson
			}
			var stats BandwidthStats
			if err := json.Unmarshal([]byte(statsJson), &stats); err != nil {
				return retErr(fmt.Sprintf("failed to parse stats: %v", err))
			}

			formatSpeed := func(bps int64) string {
				if bps >= 1024*1024 {
					return fmt.Sprintf("%.2f MB/s", float64(bps)/(1024*1024))
				} else if bps >= 1024 {
					return fmt.Sprintf("%.2f KB/s", float64(bps)/1024)
				}
				return fmt.Sprintf("%d B/s", bps)
			}

			formatSize := func(bytes int64) string {
				if bytes >= 1024*1024*1024 {
					return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
				} else if bytes >= 1024*1024 {
					return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
				} else if bytes >= 1024 {
					return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
				}
				return fmt.Sprintf("%d B", bytes)
			}

			type StatPair struct {
				Metric string `json:"metric"`
				Value  string `json:"value"`
			}

			pairs := []StatPair{
				{Metric: "Download Speed", Value: formatSpeed(stats.DownloadSpeed)},
				{Metric: "Upload Speed", Value: formatSpeed(stats.UploadSpeed)},
				{Metric: "Total Received", Value: formatSize(stats.TotalReceived)},
				{Metric: "Total Sent", Value: formatSize(stats.TotalSent)},
			}

			b, _ := json.Marshal(pairs)
			return string(b)
		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	case "service":
		switch action {
		case "discover":
			return proxyma_bind.DiscoverServices()
		case "add":
			serviceArg := args["name"]
			serviceType := args["type"]
			serviceExec := args["exec"]
			serviceDesc := args["desc"]
			serviceParams := args["param"]
			serviceNoReq := args["no-required"]
			schemaFile := args["schema-file"]

			return proxyma_bind.AddService(serviceArg, serviceType, serviceExec, serviceDesc, serviceParams, serviceNoReq, schemaFile)

		case "remove":
			serviceName := args["name"]
			return proxyma_bind.RemoveService(serviceName)

		case "run":
			serviceName := args["name"]
			if serviceName == "" {
				serviceName = args["service"]
			}
			inputsRaw := args["inputs"]
			if inputsRaw == "" {
				inputsRaw = args["payload"]
			}
			if inputsRaw == "" {
				inputsRaw = args["param"]
			}
			if inputsRaw == "" && args["input"] != "" {
				inputsRaw = "input_path=" + args["input"]
			}

			payloadJSON := ParseInputsToJSON(inputsRaw)

			schema, _ := GetServiceSchemaLocal(cliStorage, serviceName)
			if schema != nil && schema.UI != nil && schema.UI.Type == "web_app" {
				if schema.UI.LocalPath != "" {
					if _, err := os.Stat(schema.UI.LocalPath); err == nil {
						if openedPath, err := openFileWithSystemDefault(cliStorage, serviceName+"_web_ui.html", schema.UI.LocalPath); err == nil {
							fmt.Printf("🌐 Delegated Web UI opened in system default browser: %s\n\n", openedPath)
						}
					}
				}
			}

			if schema != nil && schema.IsStreaming() {
				done := make(chan struct{})
				listener := &cliStreamListener{
					onChunkFunc: func(chunk string) {
						fmt.Println(chunk)
					},
					onDoneFunc: func() {
						close(done)
					},
				}

				res := proxyma_bind.StreamService(serviceName, payloadJSON, listener)
				if strings.Contains(res, `"error":`) {
					return res
				}

				<-done
				return retMsg("Streaming completed.")
			}

			return proxyma_bind.RunService(serviceName, payloadJSON)

		case "status":
			taskID := args["task_id"]
			return proxyma_bind.GetTaskStatus(taskID)

		case "add_pipeline":
			return proxyma_bind.AddPipeline(args["id"], args["schema-file"])

		case "remove_pipeline":
			return proxyma_bind.RemovePipeline(args["id"])

		case "list_pipelines":
			return proxyma_bind.ListPipelines()

		case "get_pipeline":
			return proxyma_bind.GetPipelineSchemaJson(args["id"])

		case "clone_pipeline":
			id := args["id"]
			newID := args["new_id"]
			targetNode := args["target_node"]
			clonedJson := proxyma_bind.ClonePipelineSchemaJson(id, newID, targetNode)
			if strings.Contains(clonedJson, `"error":`) {
				return clonedJson
			}
			errStr := proxyma_bind.AddPipelineRaw(newID, clonedJson)
			if errStr != "" {
				return retErr(errStr)
			}
			return retMsg(fmt.Sprintf("Pipeline '%s' successfully cloned and registered locally!", id))

		case "run_pipeline":
			return proxyma_bind.RunPipeline(args["id"], args["payload"])

		case "edit_pipeline":
			fileID := args["file"]
			id := args["id"]
			if fileID == "" && id != "" && (strings.HasSuffix(id, ".json") || fileExists(id)) {
				fileID = id
				id = ""
			}
			return launchEditor(id, fileID)

		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	default:
		return retErr(fmt.Sprintf("unknown domain '%s'", domain))
	}
}

func launchEditor(pipelineID string, fileToOpen string) string {
	binaryPath := "/home/drusila/Projects/proxyma-services/editor/proxyma-editor"
	if _, err := os.Stat(binaryPath); err != nil {
		return fmt.Sprintf(`{"error": "Editor binary not found. Please compile it first: %v"}`, err)
	}

	cmdArgs := []string{"--storage", cliStorage}
	if pipelineID != "" {
		cmdArgs = append(cmdArgs, "--id", pipelineID)
	}
	if fileToOpen != "" {
		cmdArgs = append(cmdArgs, "--file", fileToOpen)
	}

	cmd := exec.Command(binaryPath, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Sprintf(`{"error": "Failed to run editor: %v"}`, err)
	}
	return `{"message": "Editor closed"}`
}

func isTerminalInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

type cliStreamListener struct {
	onChunkFunc func(chunk string)
	onDoneFunc  func()
}

func (l *cliStreamListener) OnChunk(chunkJSON string) {
	if l.onChunkFunc != nil {
		l.onChunkFunc(chunkJSON)
	}
}

func (l *cliStreamListener) OnError(errMsg string) {
	fmt.Fprintf(os.Stderr, "Stream Error: %s\n", errMsg)
	if l.onDoneFunc != nil {
		l.onDoneFunc()
	}
}

func (l *cliStreamListener) OnComplete() {
	if l.onDoneFunc != nil {
		l.onDoneFunc()
	}
}

func openFileWithSystemDefault(storageDir string, name string, localBlobPath string) (string, error) {
	if _, err := os.Stat(localBlobPath); err != nil {
		return "", fmt.Errorf("local cache file not found: %w", err)
	}

	previewDir := filepath.Join(storageDir, "preview")
	_ = os.MkdirAll(previewDir, 0755)

	targetPath := filepath.Join(previewDir, filepath.Base(name))
	_ = os.Remove(targetPath)

	// Symlink to preserve file extension
	if err := os.Symlink(localBlobPath, targetPath); err != nil {
		src, err := os.Open(localBlobPath)
		if err == nil {
			dst, err := os.Create(targetPath)
			if err == nil {
				_, _ = io.Copy(dst, src)
				_ = dst.Close()
			}
			_ = src.Close()
		}
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", targetPath)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", targetPath)
	default:
		cmd = exec.Command("xdg-open", targetPath)
	}

	if err := cmd.Start(); err != nil {
		return targetPath, fmt.Errorf("failed to launch system viewer (%s): %w", cmd.Path, err)
	}
	return targetPath, nil
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
