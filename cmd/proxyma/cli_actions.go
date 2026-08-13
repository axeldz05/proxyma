package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"
	"proxyma/shared/uischema"

	"github.com/spf13/cobra"
)

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

type cliStreamStart func(string, string, proxyma_bind.StreamEventListener) string

var (
	joinCluster             = proxyma_bind.JoinCluster
	clonePipelineSchemaJSON = proxyma_bind.ClonePipelineSchemaJson
	addPipelineRaw          = proxyma_bind.AddPipelineRaw
)

type cliActionStreamListener struct {
	stdout   io.Writer
	stderr   io.Writer
	terminal chan error
	once     sync.Once
}

func (l *cliActionStreamListener) OnChunk(chunkJSON string) {
	_, _ = fmt.Fprintln(l.stdout, chunkJSON)
}

func (l *cliActionStreamListener) OnError(message string) {
	l.once.Do(func() { l.terminal <- fmt.Errorf("%s", message) })
}

func (l *cliActionStreamListener) OnComplete() {
	l.once.Do(func() { l.terminal <- nil })
}

func executeCLIServiceStream(
	serviceName string,
	payloadJSON string,
	start cliStreamStart,
	stdout io.Writer,
	stderr io.Writer,
) string {
	listener := &cliActionStreamListener{
		stdout:   stdout,
		stderr:   stderr,
		terminal: make(chan error, 1),
	}
	result := start(serviceName, payloadJSON, listener)
	if proxyma_bind.IsBindError(result) {
		return result
	}
	if err := <-listener.terminal; err != nil {
		return retErr(err.Error())
	}
	return proxyma_bind.BindMessageJSON("Streaming completed.")
}

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
		errStr := joinCluster(cliStorage, token, args["node_id"], port)
		if errStr == "" {
			return proxyma_bind.BindMessageJSON("Joined cluster successfully!")
		}
		if proxyma_bind.IsBindError(errStr) {
			return errStr
		}
		return retErr(errStr)
	},

	"service.run": func(args map[string]string) string {
		norm, err := proxyma_bind.NormalizeActionArgs("service", "run", args)
		if err != nil {
			return retErr(err.Error())
		}
		serviceName := norm["service"]
		payloadJSON := norm["payload"]

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
			return executeCLIServiceStream(
				serviceName,
				payloadJSON,
				proxyma_bind.StreamService,
				os.Stdout,
				os.Stderr,
			)
		}

		return proxyma_bind.RunServiceWithStrategy(serviceName, payloadJSON, args["strategy"])
	},

	"service.clone_pipeline": func(args map[string]string) string {
		id := args["id"]
		newID := args["new_id"]
		targetNode := args["target_node"]
		clonedJSON := clonePipelineSchemaJSON(id, newID, targetNode)
		if proxyma_bind.IsBindError(clonedJSON) {
			return clonedJSON
		}
		var cloned protocol.PipelineSchema
		if err := json.Unmarshal([]byte(clonedJSON), &cloned); err != nil {
			return retErr(fmt.Sprintf("invalid cloned pipeline schema: %v", err))
		}
		if cloned.ID == "" {
			return retErr("cloned pipeline schema has no ID")
		}
		if addResult := addPipelineRaw(cloned.ID, clonedJSON); proxyma_bind.IsBindError(addResult) {
			return addResult
		}
		return clonedJSON
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

// cliRegistry is the CLI projection of the shared action registry. Local
// actions remain declared there with Surfaces=["cli"] and no UnixAction.
func cliRegistry() []uischema.DomainDetail {
	return uischema.VisibleRegistry("cli")
}

func executeActionLocal(domain string, action string, args map[string]string) string {
	detail, ok := uischema.FindAction(domain, action)
	if !ok {
		return retErr(fmt.Sprintf("unknown action '%s' for domain '%s'", action, domain))
	}
	norm, err := proxyma_bind.NormalizeActionArgs(domain, action, args)
	if err != nil {
		return retErr(err.Error())
	}
	norm, err = uischema.ValidateActionArgs(detail, norm)
	if err != nil {
		return retErr(err.Error())
	}

	if esc, ok := cliEscapes[detail.Key()]; ok {
		return esc(norm)
	}
	if detail.UnixAction == "" {
		return retErr(fmt.Sprintf("no CLI handler for '%s'", detail.Key()))
	}
	return proxyma_bind.InvokeDomainActionPrepared(domain, action, norm)
}
