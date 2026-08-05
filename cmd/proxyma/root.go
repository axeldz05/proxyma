package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
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
					svcName := resolveServiceNameFromFlags(c)
					if svcName != "" {
						payloadVal := resolvePayloadFromFlags(c)
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
					svcName := resolveServiceName(argsMap)
					payloadRaw := resolvePayloadRaw(argsMap)
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
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
