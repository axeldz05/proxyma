package proxyma

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"proxyma/internal/protocol"

	"github.com/spf13/cobra"
)

type LocalService struct {
	Type   string                 `json:"type"`
	Exec   string                 `json:"exec,omitempty"`
	Schema protocol.ServiceSchema `json:"schema"`
}

var (
	serviceStorage string
	serviceType    string
	serviceExec    string
	serviceDesc    string
	serviceParams  string
	serviceNoReq   string
	schemaFile     string
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manages custom services of the local node",
}

var addServiceCmd = &cobra.Command{
	Use:   "add [service_name_or_file.json]",
	Short: "Add a new service to the local node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serviceArg := args[0]

		_ = loadConfigOrDie(serviceStorage)

		servicesFile := filepath.Join(serviceStorage, "services.json")
		services := make(map[string]LocalService)

		if data, err := os.ReadFile(servicesFile); err == nil {
			_ = json.Unmarshal(data, &services)
		}

		var localService LocalService
		var serviceName string

		if strings.HasSuffix(serviceArg, ".json") {
			data, err := os.ReadFile(serviceArg)
			if err != nil {
				return fmt.Errorf("❌ Couldn't read service file: %v", err)
			}
			if err := json.Unmarshal(data, &localService); err != nil {
				return fmt.Errorf("❌ Invalid file format: %v", err)
			}
			serviceName = localService.Schema.Name
			if serviceName == "" {
				return fmt.Errorf("❌ Service name is missing in JSON schema")
			}
			// Allow CLI flags to override JSON values if explicitly provided
			if serviceType != "exec" && localService.Type == "" {
				localService.Type = serviceType
			}
			if serviceExec != "" {
				localService.Exec = serviceExec
			}
		} else {
			serviceName = serviceArg
			schema := protocol.ServiceSchema{
				Name:        serviceName,
				Description: serviceDesc,
				Parameters:  make(map[string]protocol.ServiceParameter),
			}

			if schemaFile != "" {
				data, err := os.ReadFile(schemaFile)
				if err != nil {
					return fmt.Errorf("❌ Couldn't read the schema file: %v", err)
				}
				if err := json.Unmarshal(data, &schema); err != nil {
					return fmt.Errorf("❌ Invalid file format: %v", err)
				}
			} else {
				noReqMap := make(map[string]bool)
				if serviceNoReq != "" {
					for _, p := range strings.Split(serviceNoReq, ",") {
						noReqMap[strings.TrimSpace(p)] = true
					}
				}

				if serviceParams != "" {
					for _, p := range strings.Split(serviceParams, ",") {
						parts := strings.Split(p, ":")
						if len(parts) < 2 {
							return fmt.Errorf("❌ Invalid parameter format '%s'. Use name:type", p)
						}

						paramName := strings.TrimSpace(parts[0])
						paramType := strings.TrimSpace(parts[1])

						isRequired := !noReqMap[paramName]

						schema.Parameters[paramName] = protocol.ServiceParameter{
							Type:     paramType,
							Required: isRequired,
						}
					}
				}
			}

			localService = LocalService{
				Type:   serviceType,
				Exec:   serviceExec,
				Schema: schema,
			}
		}

		services[serviceName] = localService

		newData, _ := json.MarshalIndent(services, "", "  ")
		if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
			return fmt.Errorf("❌ Error saving %s: %v", servicesFile, err)
		}

		fmt.Printf("✅ Service '%s' added successfully.\n", serviceName)
		fmt.Println("🔄 Restart the node ('proxyma run') to load the changes.")
		return nil
	},
}

var removeServiceCmd = &cobra.Command{
	Use:   "remove [service_name]",
	Short: "Remove a service from the local node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serviceName := args[0]
		_ = loadConfigOrDie(serviceStorage)

		servicesFile := filepath.Join(serviceStorage, "services.json")
		services := make(map[string]LocalService)

		if data, err := os.ReadFile(servicesFile); err == nil {
			_ = json.Unmarshal(data, &services)
		}

		if _, exists := services[serviceName]; !exists {
			return fmt.Errorf("❌ Service '%s' not found", serviceName)
		}

		delete(services, serviceName)

		newData, _ := json.MarshalIndent(services, "", "  ")
		if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
			return fmt.Errorf("❌ Error saving %s: %v", servicesFile, err)
		}

		fmt.Printf("✅ Service '%s' removed successfully.\n", serviceName)
		fmt.Println("🔄 Restart the node ('proxyma run') to load the changes.")
		return nil
	},
}

var discoverServiceCmd = &cobra.Command{
	Use:   "discover",
	Short: "Query active services in the cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := sendUnixSocketCommand(serviceStorage, "service_discover", nil)
		if err != nil {
			return err
		}

		var services []string
		if err := json.Unmarshal(data, &services); err != nil {
			return fmt.Errorf("failed to parse services list: %w", err)
		}

		if len(services) == 0 {
			fmt.Println("No active services found in the cluster.")
			return nil
		}

		fmt.Println("Active services in cluster:")
		for _, svc := range services {
			fmt.Printf(" - %s\n", svc)
		}
		return nil
	},
}

var runServiceCmd = &cobra.Command{
	Use:   "run [service_name] [payload_json]",
	Short: "Dispatch a task execution across the cluster and wait for results",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		serviceName := args[0]
		payload := ""
		if len(args) == 2 {
			payload = args[1]
			var js map[string]any
			if err := json.Unmarshal([]byte(payload), &js); err != nil {
				return fmt.Errorf("invalid payload JSON: %w", err)
			}
		}

		fmt.Printf("🚀 Dispatching task for service '%s'...\n", serviceName)
		data, err := sendUnixSocketCommand(serviceStorage, "service_run", map[string]string{
			"service": serviceName,
			"payload": payload,
		})
		if err != nil {
			return err
		}

		var resp protocol.ServiceTaskResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("failed to parse task response: %w", err)
		}

		fmt.Printf("✅ Task Finished:\n")
		fmt.Printf("  Task ID: %s\n", resp.TaskID)
		fmt.Printf("  Status:  %s\n", resp.Status)
		if resp.Error != "" {
			fmt.Printf("  Error:   %s\n", resp.Error)
		}
		if len(resp.Outputs) > 0 {
			outBytes, _ := json.MarshalIndent(resp.Outputs, "  ", "  ")
			fmt.Printf("  Outputs:\n  %s\n", string(outBytes))
		}
		return nil
	},
}

var statusServiceCmd = &cobra.Command{
	Use:   "status [task_id]",
	Short: "Query status of a specific task execution",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		data, err := sendUnixSocketCommand(serviceStorage, "service_status", map[string]string{
			"task_id": taskID,
		})
		if err != nil {
			return err
		}

		var resp protocol.ServiceTaskResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("failed to parse task response: %w", err)
		}

		fmt.Printf("Task ID: %s\n", resp.TaskID)
		fmt.Printf("Service: %s\n", resp.Service)
		fmt.Printf("Status:  %s\n", resp.Status)
		if resp.Error != "" {
			fmt.Printf("Error:   %s\n", resp.Error)
		}
		if len(resp.Outputs) > 0 {
			outBytes, _ := json.MarshalIndent(resp.Outputs, "", "  ")
			fmt.Printf("Outputs:\n%s\n", string(outBytes))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(serviceCmd)
	serviceCmd.AddCommand(addServiceCmd)
	serviceCmd.AddCommand(removeServiceCmd)
	serviceCmd.AddCommand(discoverServiceCmd)
	serviceCmd.AddCommand(runServiceCmd)
	serviceCmd.AddCommand(statusServiceCmd)

	defaultStorage := getDefaultStorage()

	serviceCmd.PersistentFlags().StringVar(&serviceStorage, "storage", defaultStorage, "Path to local node directory")

	// Flags for 'add'
	addServiceCmd.Flags().StringVar(&serviceType, "type", "exec", "Service type (exec, grpc)")
	addServiceCmd.Flags().StringVar(&serviceExec, "exec", "", "Command to execute (e.g: 'python3 main.py')")
	addServiceCmd.Flags().StringVar(&serviceDesc, "desc", "", "Short description of the service")
	addServiceCmd.Flags().StringVar(&serviceParams, "param", "", "Adds parameters in the format 'name:type, name2:type' (e.g.: --param 'img:string, fast:bool')")
	addServiceCmd.Flags().StringVar(&serviceNoReq, "no-required", "", "List of optional parameters separated by comma (e.g.: --no-required 'fast')")
	// Optional flag for the case of importing .json
	addServiceCmd.Flags().StringVar(&schemaFile, "schema-file", "", "Path to a JSON file containing the complete ServiceSchema")
}
