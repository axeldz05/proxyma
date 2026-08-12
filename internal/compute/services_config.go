package compute

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"

	"golang.org/x/sys/unix"
)

var servicesFileLocks sync.Map

func servicesFileLock(storagePath string) *sync.RWMutex {
	path := canonicalServicesFilePath(storagePath)
	lock, _ := servicesFileLocks.LoadOrStore(path, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

func canonicalServicesFilePath(storagePath string) string {
	path := ServicesFilePath(storagePath)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(parent, filepath.Base(path))
	}
	return path
}

// lockServicesFile provides the supported cross-process services.json
// semantics on Linux/Android: every API reader takes a shared advisory flock
// and every writer/RMW takes an exclusive one. Processes bypassing these APIs
// are unsupported because POSIX locks are advisory.
func lockServicesFile(storagePath string, exclusive bool) (func() error, error) {
	acquired, unlock, err := lockServicesFileMode(storagePath, exclusive, false)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("services file lock unexpectedly unavailable")
	}
	return unlock, nil
}

func tryLockServicesFile(storagePath string, exclusive bool) (bool, func() error, error) {
	return lockServicesFileMode(storagePath, exclusive, true)
}

func lockServicesFileMode(storagePath string, exclusive, nonblocking bool) (bool, func() error, error) {
	lockPath := canonicalServicesFilePath(storagePath) + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, nil, err
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if nonblocking {
		mode |= unix.LOCK_NB
	}
	for {
		err = unix.Flock(int(file.Fd()), mode)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		if nonblocking && (err == unix.EWOULDBLOCK || err == unix.EAGAIN) {
			return false, nil, nil
		}
		return false, nil, err
	}
	var once sync.Once
	var unlockErr error
	unlock := func() error {
		once.Do(func() {
			unlockErr = errors.Join(
				unix.Flock(int(file.Fd()), unix.LOCK_UN),
				file.Close(),
			)
		})
		return unlockErr
	}
	return true, unlock, nil
}

// LoadServicesMap reads services.json (L1). Missing file yields an empty map.
func LoadServicesMap(storagePath string) (map[string]protocol.LocalService, error) {
	lock := servicesFileLock(storagePath)
	lock.RLock()
	defer lock.RUnlock()
	unlock, err := lockServicesFile(storagePath, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	return loadServicesMap(storagePath)
}

func loadServicesMap(storagePath string) (map[string]protocol.LocalService, error) {
	services := make(map[string]protocol.LocalService)
	err := utils.ReadJSONFile(ServicesFilePath(storagePath), &services)
	if err != nil {
		if os.IsNotExist(err) {
			return services, nil
		}
		return nil, err
	}
	return services, nil
}

// SaveServicesMap writes services.json (L1).
func SaveServicesMap(storagePath string, services map[string]protocol.LocalService) error {
	lock := servicesFileLock(storagePath)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := lockServicesFile(storagePath, true)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return saveServicesMap(storagePath, services)
}

func saveServicesMap(storagePath string, services map[string]protocol.LocalService) error {
	return utils.WriteJSONFile(ServicesFilePath(storagePath), services)
}

// ServicesFilePath returns the path to services.json under storagePath.
func ServicesFilePath(storagePath string) string {
	return filepath.Join(storagePath, "services.json")
}

// BuildLocalServiceFromArgs builds a LocalService from CLI/bind arguments (L2).
func BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile string) (serviceName string, localService protocol.LocalService, err error) {
	if serviceType == "" {
		serviceType = string(protocol.ServiceTypeExec)
	}

	if strings.HasSuffix(name, ".json") || schemaFile != "" {
		fileToRead := name
		if schemaFile != "" {
			fileToRead = schemaFile
		}
		if schemaFile != "" {
			var schema protocol.ServiceSchema
			if err := utils.ReadJSONFile(fileToRead, &schema); err != nil {
				return "", localService, fmt.Errorf("couldn't read service file: %w", err)
			}
			localService.Schema = schema
			serviceName = name
			localService.Schema.Name = serviceName
		} else {
			if err := utils.ReadJSONFile(fileToRead, &localService); err != nil {
				return "", localService, fmt.Errorf("couldn't read service file: %w", err)
			}
			serviceName = localService.Schema.Name
		}
		if serviceName == "" {
			return "", localService, fmt.Errorf("service name is missing in JSON schema")
		}
		if exec != "" {
			localService.Exec = exec
		}
		if localService.Type == "" {
			localService.Type = protocol.ServiceType(serviceType)
		}
		localService, err = normalizeAndValidateLocalService(serviceName, localService)
		if err != nil {
			return "", protocol.LocalService{}, err
		}
		return serviceName, localService, nil
	}

	serviceName = name
	schema := protocol.ServiceSchema{
		Name:        serviceName,
		Type:        protocol.ServiceType(serviceType).Normalize(),
		Description: desc,
		Parameters:  make(map[string]protocol.ServiceParameter),
	}

	noReqMap := make(map[string]bool)
	if noRequired != "" {
		for _, p := range strings.Split(noRequired, ",") {
			noReqMap[strings.TrimSpace(p)] = true
		}
	}

	if param != "" {
		for _, p := range strings.Split(param, ",") {
			parts := strings.Split(p, ":")
			if len(parts) < 2 {
				return "", localService, fmt.Errorf("invalid parameter format '%s'. Use name:type", p)
			}

			paramName := strings.TrimSpace(parts[0])
			paramType := strings.TrimSpace(parts[1])

			isRequired := true
			if strings.HasSuffix(paramName, "?") {
				paramName = strings.TrimSuffix(paramName, "?")
				isRequired = false
			} else if noReqMap[paramName] {
				isRequired = false
			}

			schema.Parameters[paramName] = protocol.ServiceParameter{
				Type:     paramType,
				Required: isRequired,
				UIHint:   protocol.InferUIHint(paramName, paramType),
			}
		}
	}

	localService = protocol.LocalService{
		Type:   protocol.ServiceType(serviceType).Normalize(),
		Exec:   exec,
		Schema: schema,
	}
	localService, err = normalizeAndValidateLocalService(serviceName, localService)
	if err != nil {
		return "", protocol.LocalService{}, err
	}
	return serviceName, localService, nil
}

// UpsertLocalService adds/updates a service in services.json (L2).
func UpsertLocalService(storagePath string, serviceName string, localService protocol.LocalService) error {
	normalized, err := normalizeAndValidateLocalService(serviceName, localService)
	if err != nil {
		return err
	}
	lock := servicesFileLock(storagePath)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := lockServicesFile(storagePath, true)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	services, err := loadServicesMap(storagePath)
	if err != nil {
		return err
	}
	services[serviceName] = normalized
	return saveServicesMap(storagePath, services)
}

// DeleteLocalService removes a service from services.json (L2).
func DeleteLocalService(storagePath, name string) error {
	lock := servicesFileLock(storagePath)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := lockServicesFile(storagePath, true)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	services, err := loadServicesMap(storagePath)
	if err != nil {
		return err
	}
	if _, exists := services[name]; !exists {
		return fmt.Errorf("service '%s' not found", name)
	}
	delete(services, name)
	return saveServicesMap(storagePath, services)
}

func normalizeAndValidateLocalService(name string, service protocol.LocalService) (protocol.LocalService, error) {
	if strings.TrimSpace(name) == "" {
		return protocol.LocalService{}, fmt.Errorf("service name is missing")
	}
	if service.Schema.Name != "" && service.Schema.Name != name {
		return protocol.LocalService{}, fmt.Errorf(
			"service schema name %q does not match registration name %q",
			service.Schema.Name,
			name,
		)
	}

	serviceType := service.Type
	if serviceType == "" {
		serviceType = service.Schema.Type
	}
	if serviceType == "" {
		return protocol.LocalService{}, fmt.Errorf("service type is missing")
	}
	serviceType = serviceType.Normalize()
	if schemaType := service.Schema.Type.Normalize(); schemaType != "" && schemaType != serviceType {
		return protocol.LocalService{}, fmt.Errorf(
			"service schema type %q does not match handler type %q",
			schemaType,
			serviceType,
		)
	}
	service.Type = serviceType
	service.Schema = protocol.NormalizeServiceSchema(name, service.Schema, serviceType)

	if strings.TrimSpace(service.Exec) == "" && serviceType != protocol.ServiceTypeScreen {
		return protocol.LocalService{}, fmt.Errorf("%s service requires a non-empty exec", serviceType)
	}
	if serviceType == protocol.ServiceTypeScreen && service.Exec != "" && service.Exec != "fake" {
		return protocol.LocalService{}, fmt.Errorf("screen source %q not supported (use fake)", service.Exec)
	}
	if _, err := BuildHandler(serviceType, service.Exec); err != nil {
		return protocol.LocalService{}, fmt.Errorf("invalid %s service handler: %w", serviceType, err)
	}
	return service, nil
}

// isHTTPExec reports whether exec is an http(s) endpoint rather than a shell command (L1).
func isHTTPExec(exec string) bool {
	return strings.HasPrefix(exec, "http://") || strings.HasPrefix(exec, "https://")
}

// requireHTTPExec rejects a non-URL exec for types that can only talk HTTP (L1).
func requireHTTPExec(exec string, serviceType protocol.ServiceType, what string) error {
	if isHTTPExec(exec) {
		return nil
	}
	return fmt.Errorf("%s requires http(s) %s, got %q", serviceType, what, exec)
}

// buildStreamOrScript uses the HTTP NDJSON bidi transport for URL execs and falls
// back to a piped script for local commands.
func buildStreamOrScript(exec string) (ServiceHandler, error) {
	if isHTTPExec(exec) {
		return BuildHTTPBidiHandler(exec, protocol.HandlerDialStream), nil
	}
	return BuildScriptHandler(exec), nil
}

// serviceTypeBuilders maps a canonical ServiceType to its handler constructor (SSOT).
// Supporting a new type means one row here plus one row in protocol.serviceTypeSpecs.
var serviceTypeBuilders = map[protocol.ServiceType]func(exec string) (ServiceHandler, error){
	protocol.ServiceTypeExec: func(exec string) (ServiceHandler, error) {
		return BuildScriptHandler(exec), nil
	},
	protocol.ServiceTypeScript: func(exec string) (ServiceHandler, error) {
		return BuildScriptHandler(exec), nil
	},
	protocol.ServiceTypeGRPC: func(exec string) (ServiceHandler, error) {
		return BuildHTTPHandler(exec, protocol.HandlerDialUnary), nil
	},
	protocol.ServiceTypeGRPCBidi: buildStreamOrScript,
	protocol.ServiceTypeBidi:     buildStreamOrScript,
	protocol.ServiceTypeServerStream: func(exec string) (ServiceHandler, error) {
		if err := requireHTTPExec(exec, protocol.ServiceTypeServerStream, "exec URL"); err != nil {
			return nil, err
		}
		return BuildHTTPServerStreamHandler(exec, protocol.HandlerDialStream), nil
	},
	protocol.ServiceTypeWebRTC: buildWebRTCService,
	protocol.ServiceTypeScreen: func(exec string) (ServiceHandler, error) {
		return BuildScreenHandler(exec, protocol.HandlerDialStream), nil
	},
}

// BuildHandler constructs a ServiceHandler from type + exec (L2).
func BuildHandler(serviceType protocol.ServiceType, exec string) (ServiceHandler, error) {
	build, ok := serviceTypeBuilders[serviceType.Normalize()]
	if !ok {
		return nil, fmt.Errorf("unknown service type: %s", serviceType)
	}
	return build(exec)
}
