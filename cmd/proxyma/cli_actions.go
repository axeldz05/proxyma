package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	retErr := func(msg string) string {
		return proxyma_bind.BindErrorJSON(fmt.Errorf("%s", msg))
	}
	retMsg := proxyma_bind.BindMessageJSON
	// runAction wraps bind helpers that return "" on success or BindErrorJSON on failure.
	runAction := func(fn func() string, successMsg string) string {
		errStr := fn()
		if errStr == "" {
			return retMsg(successMsg)
		}
		if proxyma_bind.IsBindError(errStr) {
			return errStr
		}
		return retErr(errStr)
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
			localPath := proxyma_bind.ResolveLocalBlob(name)
			if proxyma_bind.IsBindError(localPath) {
				return retErr(fmt.Sprintf("failed to resolve file '%s': %s", name, proxyma_bind.ParseBindError(localPath)))
			}
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
			if proxyma_bind.IsBindError(token) {
				return token
			}
			return retMsg(token)
		case "join":
			token := args["token"]
			port := args["port"]
			if port == "" {
				port = protocol.DefaultTCPPort
			}
			nodeID := args["node_id"]
			errStr := proxyma_bind.JoinCluster(cliStorage, token, nodeID, port)
			if errStr == "" {
				return retMsg("Joined cluster successfully!")
			}
			if proxyma_bind.IsBindError(errStr) {
				return errStr
			}
			return retErr(errStr)
		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	case "telemetry":
		switch action {
		case "logs":
			return proxyma_bind.GetLogsJson()
		case "stats":
			statsJson := proxyma_bind.GetBandwidthStatsJson()
			if proxyma_bind.IsBindError(statsJson) {
				return statsJson
			}
			var stats protocol.BandwidthStats
			if err := json.Unmarshal([]byte(statsJson), &stats); err != nil {
				return retErr(fmt.Sprintf("failed to parse stats: %v", err))
			}

			type StatPair struct {
				Metric string `json:"metric"`
				Value  string `json:"value"`
			}

			pairs := []StatPair{
				{Metric: "Download Speed", Value: formatRate(float64(stats.DownloadSpeed))},
				{Metric: "Upload Speed", Value: formatRate(float64(stats.UploadSpeed))},
				{Metric: "Total Received", Value: formatBytes(stats.TotalReceived)},
				{Metric: "Total Sent", Value: formatBytes(stats.TotalSent)},
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
				if proxyma_bind.IsBindError(res) {
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
			if proxyma_bind.IsBindError(clonedJson) {
				return clonedJson
			}
			errStr := proxyma_bind.AddPipelineRaw(newID, clonedJson)
			if errStr != "" {
				if proxyma_bind.IsBindError(errStr) {
					return errStr
				}
				return retErr(errStr)
			}
			return retMsg(fmt.Sprintf("Pipeline '%s' successfully cloned and registered locally!", id))

		case "run_pipeline":
			return proxyma_bind.RunPipeline(args["id"], args["payload"])

		case "edit_pipeline":
			fileID := args["file"]
			id := args["id"]
			if fileID == "" && id != "" {
				if resolved := resolveExistingJSONPath(id); resolved != "" {
					fileID = resolved
					id = ""
				}
			}
			return launchEditor(id, fileID)

		default:
			return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
		}

	default:
		return retErr(fmt.Sprintf("unknown domain '%s'", domain))
	}
}
