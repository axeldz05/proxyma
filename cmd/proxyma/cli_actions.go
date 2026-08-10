package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"
	"proxyma/shared/uischema"

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
	return resolveServiceName(flagArgsMap(c, "name", "service"))
}

func resolvePayloadFromFlags(c *cobra.Command) string {
	return resolvePayloadRaw(flagArgsMap(c, "inputs", "payload", "param"))
}

func flagArgsMap(c *cobra.Command, keys ...string) map[string]string {
	m := make(map[string]string, len(keys))
	for _, key := range keys {
		m[key], _ = c.Flags().GetString(key)
	}
	return m
}

func retErr(msg string) string {
	return proxyma_bind.BindErrorJSON(fmt.Errorf("%s", msg))
}

type cliActionHandler func(args map[string]string) string

// cliEscapes are UX-only overrides; everything else uses InvokeDomainAction.
var cliEscapes = map[string]cliActionHandler{
	"storage.open": func(args map[string]string) string {
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
		return proxyma_bind.BindMessageJSON(fmt.Sprintf("File '%s' fetched on-demand into cache and opened with system app at: %s", name, openedPath))
	},

	"cluster.join": func(args map[string]string) string {
		token := args["token"]
		port := args["port"]
		if port == "" {
			port = protocol.DefaultTCPPort
		}
		errStr := proxyma_bind.JoinCluster(cliStorage, token, args["node_id"], port)
		if errStr == "" {
			return proxyma_bind.BindMessageJSON("Joined cluster successfully!")
		}
		if proxyma_bind.IsBindError(errStr) {
			return errStr
		}
		return retErr(errStr)
	},

	"telemetry.stats": func(_ map[string]string) string {
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
	},

	"service.run": func(args map[string]string) string {
		serviceName := resolveServiceName(args)
		inputsRaw := resolvePayloadRaw(args)
		payloadJSON := ParseInputsToJSON(inputsRaw)

		schema, _ := lookupServiceSchema(cliStorage, serviceName)
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
			return proxyma_bind.BindMessageJSON("Streaming completed.")
		}

		return proxyma_bind.RunService(serviceName, payloadJSON, args["strategy"])
	},

	"service.clone_pipeline": func(args map[string]string) string {
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
		return proxyma_bind.BindMessageJSON(fmt.Sprintf("Pipeline '%s' successfully cloned and registered locally!", id))
	},

	"service.edit_pipeline": func(args map[string]string) string {
		fileID := args["file"]
		id := args["id"]
		if fileID == "" && id != "" {
			if resolved := resolveExistingJSONPath(id); resolved != "" {
				fileID = resolved
				id = ""
			}
		}
		return launchEditor(id, fileID)
	},
}

// CLIEscapeKeys returns escape keys for consistency tests.
func CLIEscapeKeys() map[string]struct{} {
	out := make(map[string]struct{}, len(cliEscapes))
	for k := range cliEscapes {
		out[k] = struct{}{}
	}
	return out
}

func executeActionLocal(domain string, action string, args map[string]string) string {
	detail, ok := uischema.FindAction(domain, action)
	if !ok {
		return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
	}
	args = uischema.ApplyDefaults(detail, args)
	if missing := uischema.MissingRequired(detail, args); len(missing) > 0 {
		return retErr(fmt.Sprintf("missing required parameter(s): %s", strings.Join(missing, ", ")))
	}

	if esc, ok := cliEscapes[detail.Key()]; ok {
		return esc(args)
	}
	if detail.UnixAction == "" {
		return retErr(fmt.Sprintf("no CLI handler for '%s'", detail.Key()))
	}
	return proxyma_bind.InvokeDomainAction(domain, action, args)
}
