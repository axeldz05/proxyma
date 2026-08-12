package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		Use:           "proxyma",
		Short:         "Proxyma is a distributed P2P compute and storage engine",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `A secure and distributed P2P cluster.
Allows synchronizing files and running compute tasks between nodes encrypted with mutual TLS.`,
	}
)

func init() {
	defaultStorage := getDefaultStorage()
	rootCmd.PersistentFlags().StringVar(&cliStorage, "storage", defaultStorage, "Path to the local node's directory")

	// Dynamically register Cobra commands from SSOT Registry (visible CLI + Hidden escapes)
	for _, domain := range cliRegistry() {
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
					actionCmd.Flags().Bool(paramCopy.Name, protocol.ParseDefaultBool(paramCopy.DefaultValue), paramCopy.Description)
				case "int":
					actionCmd.Flags().Int(paramCopy.Name, protocol.ParseDefaultInt(paramCopy.DefaultValue), paramCopy.Description)
				default:
					actionCmd.Flags().String(paramCopy.Name, paramCopy.DefaultValue, paramCopy.Description)
				}
				if paramCopy.Required {
					_ = actionCmd.MarkFlagRequired(paramCopy.Name)
				}
			}

			if actionCopy.Domain == "service" && actionCopy.Name == "run" {
				origHelpFunc := actionCmd.HelpFunc()
				actionCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
					flagMap := flagArgsMap(c, "name", "service", "inputs", "payload", "param", "input")
					norm, _ := proxyma_bind.NormalizeActionArgs("service", "run", flagMap)
					if svcName := norm["service"]; svcName != "" {
						handled, _ := ValidateAndPrintServiceHelp(cliStorage, svcName, norm["payload"], actionCopy.Name, true)
						if handled {
							return
						}
					}
					origHelpFunc(c, args)
				})
			}

			actionCmd.RunE = func(cmd *cobra.Command, args []string) error {
				if actionCopy.Key() != "cluster.join" {
					if err := requireConfig(cliStorage); err != nil {
						return err
					}
				}
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

				if actionCopy.Domain == "service" && actionCopy.Name == "run" {
					norm, _ := proxyma_bind.NormalizeActionArgs("service", "run", argsMap)
					handled, err := ValidateAndPrintServiceHelp(cliStorage, norm["service"], norm["payload"], actionCopy.Name, false)
					if handled && err != nil {
						return err
					}
				}

				resJSON := executeActionLocal(actionCopy.Domain, actionCopy.Name, argsMap)

				// Check for errors in response
				if proxyma_bind.IsBindError(resJSON) {
					errMsg := proxyma_bind.ParseBindError(resJSON)
					schemaFile := argsMap["schema-file"]
					if schemaFile == "" {
						schemaFile = resolveExistingJSONPath(argsMap["name"])
					}
					if schemaFile != "" {
						fmt.Printf("❌ Failed to add pipeline schema from file '%s': %s\n", schemaFile, errMsg)
						fmt.Printf("💡 Tip: You can open and edit this schema file in the visual editor by running:\n")
						fmt.Printf("   proxyma service edit_pipeline --file %s\n\n", schemaFile)

						if isTerminalInteractive() {
							fmt.Print("Would you like to open this file in the visual pipeline editor now? [y/N]: ")
							var answer string
							_, _ = fmt.Scanln(&answer)
							answer = strings.ToLower(strings.TrimSpace(answer))
							if answer == "y" || answer == "yes" {
								resJSON = launchEditor("", schemaFile)
								if !proxyma_bind.IsBindError(resJSON) {
									return nil
								}
							}
						}
					}
					return fmt.Errorf("%s", errMsg)
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

					for _, rowFields := range uischema.ProjectRows(actionCopy.Columns, list) {
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
func Execute() {
	if code := executeRoot(os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func executeRoot(stderr io.Writer) int {
	if err := rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
