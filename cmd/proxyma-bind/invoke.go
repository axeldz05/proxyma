package proxyma_bind

import (
	"encoding/json"
	"fmt"
	"strings"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/shared/uischema"
)

// InvokeDomainAction is the generic L3 interpreter: Registry → Normalize → Validate → Prepared.
// No per-action localFn; daemon logic lives only in server.unixHandlers.
func InvokeDomainAction(domain, action string, args map[string]string) string {
	detail, ok := uischema.FindAction(domain, action)
	if !ok {
		return BindErrorJSON(fmt.Errorf("unknown action %s.%s", domain, action))
	}
	if detail.UnixAction == "" {
		return BindErrorJSON(fmt.Errorf("no unix action for %s.%s", domain, action))
	}

	norm, err := NormalizeActionArgs(domain, action, args)
	if err != nil {
		return BindErrorJSON(err)
	}
	norm, err = uischema.ValidateActionArgs(detail, norm)
	if err != nil {
		return BindErrorJSON(err)
	}
	return InvokeDomainActionPrepared(domain, action, norm)
}

// InvokeDomainActionPrepared is L2: assumes args already passed Normalize→Validate
// (or are otherwise trusted). Used by CLI after its own prep to avoid a double pass.
func InvokeDomainActionPrepared(domain, action string, args map[string]string) string {
	detail, ok := uischema.FindAction(domain, action)
	if !ok {
		return BindErrorJSON(fmt.Errorf("unknown action %s.%s", domain, action))
	}
	if detail.UnixAction == "" {
		return BindErrorJSON(fmt.Errorf("no unix action for %s.%s", domain, action))
	}

	storagePath := GetStoragePath()
	offline := offlineHookForStorage(domain, action, args, storagePath)
	var raw string
	if offline != nil {
		raw = dispatchUnixLocalOrOfflineAt(storagePath, detail.UnixAction, args, func(s *server.Server) (any, error) {
			return server.CallUnixUnary(s, detail.UnixAction, args)
		}, offline)
	} else {
		raw = dispatchUnixLocalOrOfflineAt(storagePath, detail.UnixAction, args, func(s *server.Server) (any, error) {
			return server.CallUnixUnary(s, detail.UnixAction, args)
		}, nil)
	}
	if IsBindError(raw) {
		return raw
	}
	return formatActionResult(detail, args, raw)
}

// offlineHooks are headless fallbacks when neither in-process Server nor unix socket is available.
// Keys are "domain.action". Bodies call the same compute L2 as LocalService* (no *Server / no notify).
// storagePath is captured once per operation so a config alias cannot split daemon and fallback state.
var offlineHooks = map[string]func(storagePath string, args map[string]string) (any, error){
	"service.add": func(storagePath string, args map[string]string) (any, error) {
		serviceName, localService, buildErr := compute.BuildLocalServiceFromArgs(
			args["name"], args["type"], args["exec"], args["desc"],
			args["param"], args["no-required"], args["schema-file"],
		)
		if buildErr != nil {
			return nil, buildErr
		}
		if saveErr := compute.UpsertLocalService(storagePath, serviceName, localService); saveErr != nil {
			return nil, fmt.Errorf("error saving services file: %w", saveErr)
		}
		args["name"] = serviceName
		return nil, nil
	},
	"service.remove": func(storagePath string, args map[string]string) (any, error) {
		if delErr := compute.DeleteLocalService(storagePath, args["name"]); delErr != nil {
			return nil, delErr
		}
		return nil, nil
	},
	"service.detail": func(storagePath string, args map[string]string) (any, error) {
		svcs, err := compute.LoadServicesMap(storagePath)
		if err != nil {
			return nil, err
		}
		svc, ok := svcs[args["name"]]
		if !ok {
			return nil, fmt.Errorf("service %q not found offline", args["name"])
		}
		return protocol.NormalizeServiceSchema(args["name"], svc.Schema, svc.Type), nil
	},
}

func offlineHookFor(domain, action string, args map[string]string) func() (any, error) {
	return offlineHookForStorage(domain, action, args, GetStoragePath())
}

func offlineHookForStorage(
	domain string,
	action string,
	args map[string]string,
	storagePath string,
) func() (any, error) {
	h, ok := offlineHooks[domain+"."+action]
	if !ok {
		return nil
	}
	return func() (any, error) { return h(storagePath, args) }
}

func formatActionResult(detail uischema.ActionDetail, args map[string]string, raw string) string {
	if detail.OutputType != "text" {
		return raw
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		var msgEnv struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(raw), &msgEnv) == nil && msgEnv.Message != "" {
			return raw
		}
	}

	resultStr := ""
	if raw != "" && raw != `""` && raw != "null" {
		var unquoted string
		if err := json.Unmarshal([]byte(raw), &unquoted); err == nil {
			resultStr = unquoted
		} else if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			resultStr = raw
		}
	}

	msg := uischema.FormatSuccessMessage(detail, args, resultStr)
	if msg == "" {
		if resultStr != "" {
			return BindMessageJSON(resultStr)
		}
		return BindMessageJSON("ok")
	}
	return BindMessageJSON(msg)
}

// NormalizeActionArgs maps CLI/UI arg names onto unix IPC arg names and expands file payloads.
func NormalizeActionArgs(domain, action string, args map[string]string) (map[string]string, error) {
	return uischema.NormalizeActionArgs(domain, action, args)
}
