package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"proxyma/internal/protocol"
)

var (
	storagePath string
	fileToOpen  string
	pipelineID  string
	layoutsFile string
)

func main() {
	flag.StringVar(&storagePath, "storage", "/app/data", "Storage path containing "+protocol.SockFileName)
	flag.StringVar(&fileToOpen, "file", "", "Path to local pipeline schema JSON file to load")
	flag.StringVar(&fileToOpen, "f", "", "Path to local pipeline schema JSON file to load (shorthand)")
	flag.StringVar(&pipelineID, "id", "", "Pipeline ID to edit")
	flag.Parse()

	if fileToOpen == "" && len(flag.Args()) > 0 {
		arg := flag.Arg(0)
		if strings.HasSuffix(arg, ".json") || fileExists(arg) {
			fileToOpen = arg
		} else if pipelineID == "" {
			pipelineID = arg
		}
	}

	layoutsFile = filepath.Join(os.Getenv("HOME"), ".config", "proxyma", "pipeline_layouts.json")
	if err := os.MkdirAll(filepath.Dir(layoutsFile), 0755); err != nil {
		layoutsFile = "pipeline_layouts.json" // fallback to local directory
	}

	fmt.Println("====================================================")
	fmt.Println("🛠️  Proxyma Standalone Pipeline Editor")
	fmt.Println("====================================================")
	fmt.Printf("Connecting to daemon at storage path: %s\n", storagePath)

	// Fetch active services from the daemon
	services, err := fetchServices(storagePath)
	if err != nil {
		fmt.Printf("⚠️  Warning: Couldn't fetch service registry from daemon: %v\n", err)
		fmt.Println("Type checks will be skipped for unknown services.")
		services = make(map[string]protocol.ServiceSchema)
	} else {
		fmt.Printf("Found %d registered services.\n", len(services))
	}

	builder := NewBuilder("my-pipeline")
	if pipelineID != "" {
		builder.ID = pipelineID
	}

	if fileToOpen != "" {
		if err := builder.LoadFromFile(fileToOpen); err != nil {
			fmt.Printf("⚠️  Could not load file '%s': %v\n", fileToOpen, err)
		} else {
			fmt.Printf("✅ Loaded draft pipeline schema from file: %s\n", fileToOpen)
		}
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		printDashboard(builder)
		fmt.Println("\nActions:")
		fmt.Println("1. Set Pipeline properties (ID, Version)")
		fmt.Println("2. Add Step")
		fmt.Println("3. Remove Step")
		fmt.Println("4. Add Connection")
		fmt.Println("5. Remove Connection")
		fmt.Println("6. Validate Pipeline Schema (Daemon Verification)")
		fmt.Println("7. Save & Register Pipeline to Daemon")
		fmt.Println("8. Load Pipeline from Daemon")
		fmt.Println("9. Load Pipeline from Local JSON File")
		fmt.Println("10. Save Pipeline to Local JSON File")
		fmt.Println("11. Print JSON Schema")
		fmt.Println("12. Exit")
		fmt.Print("Choose action [1-12]: ")

		choice, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("\nInput stream closed. Exiting pipeline editor.")
			break
		}
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			fmt.Print("Enter Pipeline ID: ")
			id, _ := reader.ReadString('\n')
			builder.ID = strings.TrimSpace(id)

			fmt.Print("Enter Version (integer, e.g. 1): ")
			verStr, _ := reader.ReadString('\n')
			if ver, err := strconv.Atoi(strings.TrimSpace(verStr)); err == nil {
				builder.Version = ver
			}

		case "2":
			fmt.Print("Enter Step ID: ")
			stepID, _ := reader.ReadString('\n')
			stepID = strings.TrimSpace(stepID)

			if len(services) > 0 {
				fmt.Println("Available services:")
				idx := 1
				svcList := make([]string, 0)
				for name := range services {
					fmt.Printf("  %d. %s\n", idx, name)
					svcList = append(svcList, name)
					idx++
				}
				fmt.Print("Select service index: ")
				idxStr, _ := reader.ReadString('\n')
				selectedIdx, err := strconv.Atoi(strings.TrimSpace(idxStr))
				if err != nil || selectedIdx < 1 || selectedIdx > len(svcList) {
					fmt.Println("❌ Invalid selection")
					break
				}
				serviceName := svcList[selectedIdx-1]

				fmt.Print("Enter Target Node ID (press Enter for local/empty): ")
				nodeID, _ := reader.ReadString('\n')
				nodeID = strings.TrimSpace(nodeID)

				// Automatically set layout position
				x := float64(len(builder.Steps)) * 150.0
				y := 100.0

				if err := builder.AddStep(stepID, serviceName, nodeID, x, y); err != nil {
					fmt.Printf("❌ Error adding step: %v\n", err)
				} else {
					fmt.Printf("✅ Step '%s' added.\n", stepID)
				}
			} else {
				fmt.Print("Enter service name manually: ")
				svcName, _ := reader.ReadString('\n')
				svcName = strings.TrimSpace(svcName)
				if err := builder.AddStep(stepID, svcName, "", 0, 0); err != nil {
					fmt.Printf("❌ Error adding step: %v\n", err)
				}
			}

		case "3":
			fmt.Print("Enter Step ID to remove: ")
			stepID, _ := reader.ReadString('\n')
			stepID = strings.TrimSpace(stepID)
			builder.RemoveStep(stepID)
			fmt.Printf("✅ Step '%s' removed.\n", stepID)

		case "4":
			fmt.Println("--- Connect Ports ---")
			// Select Source Step
			stepsList := make([]string, 0)
			stepsList = append(stepsList, "$initial")
			for id := range builder.Steps {
				stepsList = append(stepsList, id)
			}
			fmt.Println("Source Steps:")
			for i, step := range stepsList {
				fmt.Printf("  %d. %s\n", i+1, step)
			}
			fmt.Print("Select source step index: ")
			idxStr, _ := reader.ReadString('\n')
			selectedIdx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil || selectedIdx < 1 || selectedIdx > len(stepsList) {
				fmt.Println("❌ Invalid selection")
				break
			}
			fromStep := stepsList[selectedIdx-1]

			// Determine available output ports
			var fromPort string
			if fromStep == "$initial" {
				fmt.Print("Enter initial input parameter name: ")
				portName, _ := reader.ReadString('\n')
				fromPort = strings.TrimSpace(portName)
			} else {
				fromSvc := builder.Steps[fromStep].Service
				fromSvcSchema, hasSchema := services[fromSvc]
				if hasSchema && len(fromSvcSchema.Outputs) > 0 {
					fmt.Println("Available outputs:")
					outList := make([]string, 0)
					idx := 1
					for name, p := range fromSvcSchema.Outputs {
						fmt.Printf("  %d. %s (type: %s)\n", idx, name, p.Type)
						outList = append(outList, name)
						idx++
					}
					fmt.Print("Select output port index: ")
					outIdxStr, _ := reader.ReadString('\n')
					outIdx, err := strconv.Atoi(strings.TrimSpace(outIdxStr))
					if err != nil || outIdx < 1 || outIdx > len(outList) {
						fmt.Println("❌ Invalid selection")
						break
					}
					fromPort = outList[outIdx-1]
				} else {
					fmt.Print("Enter output port name manually: ")
					portName, _ := reader.ReadString('\n')
					fromPort = strings.TrimSpace(portName)
				}
			}

			// Select Target Step
			targetSteps := make([]string, 0)
			for id := range builder.Steps {
				targetSteps = append(targetSteps, id)
			}
			if len(targetSteps) == 0 {
				fmt.Println("❌ No target steps defined in pipeline")
				break
			}
			fmt.Println("Target Steps:")
			for i, step := range targetSteps {
				fmt.Printf("  %d. %s\n", i+1, step)
			}
			fmt.Print("Select target step index: ")
			tIdxStr, _ := reader.ReadString('\n')
			tSelectedIdx, err := strconv.Atoi(strings.TrimSpace(tIdxStr))
			if err != nil || tSelectedIdx < 1 || tSelectedIdx > len(targetSteps) {
				fmt.Println("❌ Invalid selection")
				break
			}
			toStep := targetSteps[tSelectedIdx-1]

			// Select Target Input Port
			var toPort string
			toSvc := builder.Steps[toStep].Service
			toSvcSchema, hasSchema := services[toSvc]
			if hasSchema && len(toSvcSchema.Parameters) > 0 {
				fmt.Println("Available inputs:")
				inList := make([]string, 0)
				idx := 1
				for name, p := range toSvcSchema.Parameters {
					reqLabel := ""
					if p.Required {
						reqLabel = " (required)"
					}
					fmt.Printf("  %d. %s (type: %s)%s\n", idx, name, p.Type, reqLabel)
					inList = append(inList, name)
					idx++
				}
				fmt.Print("Select input port index: ")
				inIdxStr, _ := reader.ReadString('\n')
				inIdx, err := strconv.Atoi(strings.TrimSpace(inIdxStr))
				if err != nil || inIdx < 1 || inIdx > len(inList) {
					fmt.Println("❌ Invalid selection")
					break
				}
				toPort = inList[inIdx-1]
			} else {
				fmt.Print("Enter input port name manually: ")
				portName, _ := reader.ReadString('\n')
				toPort = strings.TrimSpace(portName)
			}

			// Connect
			if err := builder.Connect(fromStep, fromPort, toStep, toPort, services); err != nil {
				fmt.Printf("❌ Connection Rejected: %v\n", err)
			} else {
				fmt.Println("✅ Ports connected successfully.")
			}

		case "5":
			if len(builder.Connections) == 0 {
				fmt.Println("No connections to remove.")
				break
			}
			fmt.Println("Connections:")
			for i, conn := range builder.Connections {
				fmt.Printf("  %d. [%s].%s ──► [%s].%s\n", i+1, conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort)
			}
			fmt.Print("Select connection index to remove: ")
			idxStr, _ := reader.ReadString('\n')
			selectedIdx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil || selectedIdx < 1 || selectedIdx > len(builder.Connections) {
				fmt.Println("❌ Invalid selection")
				break
			}
			conn := builder.Connections[selectedIdx-1]
			builder.Disconnect(conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort)
			fmt.Println("✅ Connection removed.")

		case "6":
			if builder.ID == "" {
				fmt.Println("❌ Error: Pipeline ID cannot be empty.")
				break
			}
			schema := builder.Export()
			schemaBytes, _ := json.Marshal(schema)

			fmt.Println("🔍 Validating pipeline schema...")
			err = sendUnixSocketCommand(storagePath, "pipeline_validate", map[string]string{
				"schema": string(schemaBytes),
			})
			if err != nil {
				if isConnectionRefused(err) {
					fmt.Printf("⚠️  Daemon is unreachable at '%s' (offline mode).\n", storagePath)
					fmt.Println("🔍 Running offline local graph & structural validation...")
					if localErr := builder.ValidateLocal(services); localErr != nil {
						fmt.Printf("❌ Offline Validation Error: %v\n", localErr)
					} else {
						fmt.Println("✅ Pipeline graph structure & local types are 100% VALID!")
						fmt.Println("💡 Tip: Start the daemon (e.g. run './scripts/bootstrap_dev.sh') to register and execute this pipeline.")
					}
				} else {
					fmt.Printf("❌ Validation Failed: %v\n", err)
				}
			} else {
				fmt.Println("✅ Pipeline schema is 100% VALID according to the daemon!")
			}

		case "7":
			if builder.ID == "" {
				fmt.Println("❌ Error: Pipeline ID cannot be empty.")
				break
			}
			schema := builder.Export()
			schemaBytes, _ := json.Marshal(schema)

			fmt.Println("Registering pipeline in daemon...")
			err = sendUnixSocketCommand(storagePath, "pipeline_add", map[string]string{
				"schema": string(schemaBytes),
			})
			if err != nil {
				if isConnectionRefused(err) {
					fmt.Printf("❌ Failed to register pipeline: Daemon is unreachable at '%s'.\n", storagePath)
					fmt.Println("💡 Tip: Run './scripts/bootstrap_dev.sh' or 'proxyma run' to start the daemon.")
				} else {
					fmt.Printf("❌ Failed to register pipeline: %v\n", err)
				}
			} else {
				fmt.Println("✅ Pipeline schema registered successfully in daemon VFS/Compute!")
				if err := saveLayouts(builder.ID, builder.Layout); err != nil {
					fmt.Printf("⚠️  Warning: Couldn't save layouts metadata: %v\n", err)
				}
			}

		case "8":
			fmt.Println("Fetching pipeline schemas from daemon...")
			pipelines, err := fetchPipelines(storagePath)
			if err != nil {
				if isConnectionRefused(err) {
					fmt.Printf("❌ Cannot fetch pipelines: Daemon is unreachable at '%s'.\n", storagePath)
					fmt.Println("💡 Tip: Run './scripts/bootstrap_dev.sh' or 'proxyma run' to start the daemon.")
				} else {
					fmt.Printf("❌ Failed to retrieve pipelines: %v\n", err)
				}
				break
			}
			if len(pipelines) == 0 {
				fmt.Println("No pipelines found registered in daemon.")
				break
			}
			fmt.Println("Available pipelines:")
			for i, p := range pipelines {
				fmt.Printf("  %d. %s (Version: %d, Steps: %d)\n", i+1, p.ID, p.Version, len(p.Steps))
			}
			fmt.Print("Select pipeline index to load: ")
			idxStr, _ := reader.ReadString('\n')
			selectedIdx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil || selectedIdx < 1 || selectedIdx > len(pipelines) {
				fmt.Println("❌ Invalid selection")
				break
			}
			loaded := pipelines[selectedIdx-1]

			// Load into builder
			builder = NewBuilder(loaded.ID)
			builder.Version = loaded.Version
			for _, step := range loaded.Steps {
				builder.Steps[step.ID] = step
			}
			builder.Connections = loaded.Connections

			// Try to load layout metadata
			if layout, err := loadLayouts(builder.ID); err == nil {
				builder.Layout = layout
			} else {
				// Re-generate default coordinates if layout file not found
				for id := range builder.Steps {
					builder.Layout[id] = NodePosition{X: float64(len(builder.Layout)) * 150.0, Y: 100.0}
				}
			}
			fmt.Printf("✅ Pipeline '%s' loaded successfully.\n", builder.ID)

		case "9":
			fmt.Print("Enter local JSON file path to load: ")
			path, _ := reader.ReadString('\n')
			path = strings.TrimSpace(path)
			if path == "" {
				fmt.Println("❌ File path cannot be empty.")
				break
			}
			if err := builder.LoadFromFile(path); err != nil {
				fmt.Printf("❌ Failed to load file: %v\n", err)
			} else {
				fmt.Printf("✅ Pipeline schema loaded from file '%s'.\n", path)
				fileToOpen = path
			}

		case "10":
			defaultPath := fileToOpen
			if defaultPath == "" {
				defaultPath = builder.ID + ".json"
			}
			fmt.Printf("Enter local JSON file path to save [default: %s]: ", defaultPath)
			path, _ := reader.ReadString('\n')
			path = strings.TrimSpace(path)
			if path == "" {
				path = defaultPath
			}
			if err := builder.SaveToFile(path); err != nil {
				fmt.Printf("❌ Failed to save file: %v\n", err)
			} else {
				fmt.Printf("✅ Pipeline schema saved to file '%s'.\n", path)
				fileToOpen = path
			}

		case "11":
			schema := builder.Export()
			b, _ := json.MarshalIndent(schema, "", "  ")
			fmt.Println("\nLogical Pipeline JSON Schema:")
			fmt.Println(string(b))

		case "12":
			fmt.Println("Exiting pipeline editor.")
			return

		default:
			fmt.Println("❌ Invalid command index.")
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such file or directory")
}

func printDashboard(b *Builder) {
	fmt.Println("\n----------------------------------------------------")
	fmt.Printf(" Pipeline: %s (Version: %d)\n", b.ID, b.Version)
	fmt.Println("----------------------------------------------------")
	fmt.Println("Steps:")
	if len(b.Steps) == 0 {
		fmt.Println("  (No steps defined)")
	} else {
		for id, step := range b.Steps {
			nodeInfo := ""
			if step.TargetNodeID != "" {
				nodeInfo = fmt.Sprintf(" [Run on: %s]", step.TargetNodeID)
			}
			pos := b.Layout[id]
			fmt.Printf("  • [%s] running service '%s'%s (pos: %.0f, %.0f)\n", id, step.Service, nodeInfo, pos.X, pos.Y)
		}
	}
	fmt.Println("\nConnections:")
	if len(b.Connections) == 0 {
		fmt.Println("  (No connections defined)")
	} else {
		for _, conn := range b.Connections {
			fmt.Printf("  [%s].%s ──────► [%s].%s\n", conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort)
		}
	}
	fmt.Println("----------------------------------------------------")
}

// dialUnary is the editor-local L2 over protocol unix types (SSOT framing/sock in protocol).
func dialUnary(storage, action string, args map[string]string) (json.RawMessage, error) {
	conn, err := net.Dial("unix", protocol.UnixSockPath(storage))
	if err != nil {
		return nil, fmt.Errorf("daemon is unreachable: %w", err)
	}
	defer func() { _ = conn.Close() }()

	reqBytes, err := json.Marshal(protocol.UnixRequest{Action: action, Args: args})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(reqBytes); err != nil {
		return nil, err
	}

	var respBytes []byte
	buf := make([]byte, 4096)
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			respBytes = append(respBytes, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}

	var resp protocol.UnixResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse daemon response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("daemon error: %s", resp.Error)
	}
	return resp.Data, nil
}

func sendUnixSocketCommand(storage string, action string, args map[string]string) error {
	_, err := dialUnary(storage, action, args)
	return err
}

func fetchServices(storage string) (map[string]protocol.ServiceSchema, error) {
	data, err := dialUnary(storage, "service_discover", nil)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, err
	}
	res := make(map[string]protocol.ServiceSchema)
	for _, name := range names {
		schema, err := fetchServiceDetail(storage, name)
		if err == nil {
			res[name] = schema
		}
	}
	return res, nil
}

func fetchServiceDetail(storage string, name string) (protocol.ServiceSchema, error) {
	data, err := dialUnary(storage, "service_detail", map[string]string{"name": name})
	if err != nil {
		return protocol.ServiceSchema{}, err
	}
	var schema protocol.ServiceSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return protocol.ServiceSchema{}, err
	}
	return schema, nil
}

func fetchPipelines(storage string) ([]protocol.PipelineSchema, error) {
	data, err := dialUnary(storage, "pipeline_list", nil)
	if err != nil {
		return nil, err
	}
	var list []protocol.PipelineSchema
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func saveLayouts(pipelineID string, layout map[string]NodePosition) error {
	allLayouts := make(map[string]map[string]NodePosition)
	if data, err := os.ReadFile(layoutsFile); err == nil {
		_ = json.Unmarshal(data, &allLayouts)
	}
	allLayouts[pipelineID] = layout
	b, err := json.MarshalIndent(allLayouts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(layoutsFile, b, 0644)
}

func loadLayouts(pipelineID string) (map[string]NodePosition, error) {
	allLayouts := make(map[string]map[string]NodePosition)
	data, err := os.ReadFile(layoutsFile)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &allLayouts); err != nil {
		return nil, err
	}
	layout, exists := allLayouts[pipelineID]
	if !exists {
		return nil, fmt.Errorf("layout not found")
	}
	return layout, nil
}
