package cmd

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
	Short: "Manages personalized services of local node",
}

var addServiceCmd = &cobra.Command{
	Use:   "add [nombre_servicio_o_archivo.json]",
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
					return fmt.Errorf("❌ Couldn't read the squeme file: %v", err)
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
	// (El comando remove queda casi idéntico al que armamos antes,
	// pero usando `serviceStorage` y validando LoadConfig)
}

func init() {
	rootCmd.AddCommand(serviceCmd)
	serviceCmd.AddCommand(addServiceCmd)
	serviceCmd.AddCommand(removeServiceCmd)

	defaultStorage := getDefaultStorage()

	serviceCmd.PersistentFlags().StringVar(&serviceStorage, "storage", defaultStorage, "Path to local node directory")

	// Flags for 'add'
	addServiceCmd.Flags().StringVar(&serviceType, "type", "exec", "Service type (exec, grpc)")
	addServiceCmd.Flags().StringVar(&serviceExec, "exec", "", "Command to execute (e.g: 'python3 main.py')")
	addServiceCmd.Flags().StringVar(&serviceDesc, "desc", "", "Short description of the service")
	addServiceCmd.Flags().StringVar(&serviceParams, "param", "", "Añade parámetros en formato 'nombre: tipo, nombre2: tipo' (ej: --param 'img:string, fast:bool')")
	addServiceCmd.Flags().StringVar(&serviceNoReq, "no-required", "", "Lista de parámetros opcionales separados por coma (ej: --no-required 'fast')")
	// Optional flag for the case of importing .json
	addServiceCmd.Flags().StringVar(&schemaFile, "schema-file", "", "Ruta a un archivo JSON con el ServiceSchema completo")
}
