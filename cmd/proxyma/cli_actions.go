package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolveServiceName(args map[string]string) string {
	return firstNonEmpty(args["name"], args["service"])
}

func resolvePayloadRaw(args map[string]string) string {
	if raw := firstNonEmpty(args["inputs"], args["payload"], args["param"]); raw != "" {
		return raw
	}
	if args["input"] != "" {
		return "input_path=" + args["input"]
	}
	return ""
}

func resolveServiceNameFromFlags(c *cobra.Command) string {
	name, _ := c.Flags().GetString("name")
	if name == "" {
		name, _ = c.Flags().GetString("service")
	}
	return name
}

func resolvePayloadFromFlags(c *cobra.Command) string {
	for _, key := range []string{"inputs", "payload", "param"} {
		if v, _ := c.Flags().GetString(key); v != "" {
			return v
		}
	}
	return ""
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
			if strings.Contains(token, `"error"`) {
				return retErr(proxyma_bind.ParseBindError(token))
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
				return retErr(proxyma_bind.ParseBindError(errStr))
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
			var stats protocol.BandwidthStats
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
			serviceName := resolveServiceName(args)
			inputsRaw := resolvePayloadRaw(args)
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
