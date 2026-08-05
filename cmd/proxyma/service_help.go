package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"
)

// GetServiceSchemaLocal fetches the ServiceSchema for a target service from the daemon or local registry.
func GetServiceSchemaLocal(storagePath string, serviceName string) (*protocol.ServiceSchema, error) {
	proxyma_bind.SetStoragePath(storagePath)
	detailsJSON := proxyma_bind.GetServiceDetails(serviceName)

	var schema protocol.ServiceSchema
	if err := json.Unmarshal([]byte(detailsJSON), &schema); err == nil && (schema.Name != "" || len(schema.Parameters) > 0) {
		if schema.Name == "" {
			schema.Name = serviceName
		}
		return &schema, nil
	}

	// Fallback: Read services.json directly from storagePath if daemon unreachable
	svcFile := filepath.Join(storagePath, "services.json")
	data, err := os.ReadFile(svcFile)
	if err == nil {
		var svcs map[string]struct {
			Schema protocol.ServiceSchema `json:"schema"`
		}
		if err := json.Unmarshal(data, &svcs); err == nil {
			if svc, ok := svcs[serviceName]; ok {
				s := svc.Schema
				if s.Name == "" {
					s.Name = serviceName
				}
				return &s, nil
			}
		}
	}

	return nil, fmt.Errorf("service '%s' details unavailable", serviceName)
}

// ParseInputsToJSON converts key1=val1,key2=val2 key-value strings or JSON objects into a valid JSON string.
func ParseInputsToJSON(inputsRaw string) string {
	inputsRaw = strings.TrimSpace(inputsRaw)
	if inputsRaw == "" {
		return "{}"
	}
	if strings.HasPrefix(inputsRaw, "{") && strings.HasSuffix(inputsRaw, "}") {
		return inputsRaw
	}

	payload := make(map[string]any)
	pairs := strings.Split(inputsRaw, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) < 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		vStr := strings.TrimSpace(parts[1])

		if k == "" {
			continue
		}

		if vStr == "true" {
			payload[k] = true
		} else if vStr == "false" {
			payload[k] = false
		} else if num, err := strconv.ParseFloat(vStr, 64); err == nil && !strings.Contains(vStr, " ") {
			if float64(int64(num)) == num {
				payload[k] = int64(num)
			} else {
				payload[k] = num
			}
		} else {
			payload[k] = vStr
		}
	}

	b, _ := json.Marshal(payload)
	return string(b)
}

// BuildSampleKVInputs generates a representative key1=val1,key2=val2 string sample.
func BuildSampleKVInputs(schema *protocol.ServiceSchema) string {
	keys := make([]string, 0, len(schema.Parameters))
	for k := range schema.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var kvPairs []string
	for _, k := range keys {
		param := schema.Parameters[k]
		valStr := "example_value"
		if param.Default != "" {
			valStr = param.Default
		} else {
			lowerKey := strings.ToLower(k)
			switch param.Type {
			case "bool":
				valStr = "true"
			case "int":
				valStr = "100"
			case "file":
				valStr = "/path/to/input_file"
			default:
				if strings.Contains(lowerKey, "path") || strings.Contains(lowerKey, "file") || strings.Contains(lowerKey, "doc") {
					valStr = "/path/to/input_file"
				} else if strings.Contains(lowerKey, "lang") {
					valStr = "eng"
				} else if strings.Contains(lowerKey, "user") || strings.Contains(lowerKey, "name") {
					valStr = "user_example"
				} else if len(param.Options) > 0 {
					valStr = param.Options[0]
				}
			}
		}
		kvPairs = append(kvPairs, fmt.Sprintf("%s=%s", k, valStr))
	}

	return strings.Join(kvPairs, ",")
}

// BuildSampleJSONPayload constructs a representative JSON sample based on parameter definitions.
func BuildSampleJSONPayload(schema *protocol.ServiceSchema) string {
	sample := make(map[string]any)

	// Sort parameter names for deterministic output
	keys := make([]string, 0, len(schema.Parameters))
	for k := range schema.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		param := schema.Parameters[k]
		if param.Default != "" {
			switch param.Type {
			case "bool":
				sample[k] = param.Default == "true" || param.Default == "1"
			case "int":
				var val int
				_, _ = fmt.Sscanf(param.Default, "%d", &val)
				sample[k] = val
			default:
				sample[k] = param.Default
			}
			continue
		}

		lowerKey := strings.ToLower(k)
		switch param.Type {
		case "bool":
			sample[k] = true
		case "int":
			sample[k] = 100
		case "file":
			sample[k] = "/path/to/input_file"
		default:
			if strings.Contains(lowerKey, "path") || strings.Contains(lowerKey, "file") || strings.Contains(lowerKey, "doc") {
				sample[k] = "/path/to/input_file"
			} else if strings.Contains(lowerKey, "lang") {
				sample[k] = "eng"
			} else if strings.Contains(lowerKey, "user") || strings.Contains(lowerKey, "name") {
				sample[k] = "user_example"
			} else if len(param.Options) > 0 {
				sample[k] = param.Options[0]
			} else {
				sample[k] = "example_value"
			}
		}
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(sample)
	return strings.TrimSpace(buf.String())
}

// ValidateAndPrintServiceHelp validates payload against ServiceSchema and prints contextual help block.
// Returns handled (bool) and error if validation failed.
func ValidateAndPrintServiceHelp(storagePath string, serviceName string, payloadRaw string, actionName string, isHelpRequested bool) (bool, error) {
	if serviceName == "" {
		return false, nil
	}

	jsonStr := ParseInputsToJSON(payloadRaw)
	var parsedPayload map[string]any
	if jsonStr != "" && jsonStr != "{}" {
		_ = json.Unmarshal([]byte(jsonStr), &parsedPayload)
	}

	// Fast-path: If user provided a non-empty payload and did NOT ask for help (-h/--help),
	// do not block or query service details.
	if !isHelpRequested && len(parsedPayload) > 0 {
		return false, nil
	}

	schema, err := GetServiceSchemaLocal(storagePath, serviceName)
	if err != nil || schema == nil {
		return false, nil
	}

	var missingRequired []string
	paramKeys := make([]string, 0, len(schema.Parameters))
	for k := range schema.Parameters {
		paramKeys = append(paramKeys, k)
	}
	sort.Strings(paramKeys)

	for _, k := range paramKeys {
		param := schema.Parameters[k]
		if param.Required {
			val, exists := parsedPayload[k]
			if !exists || val == nil || fmt.Sprintf("%v", val) == "" {
				missingRequired = append(missingRequired, k)
			}
		}
	}

	shouldDisplayHelp := isHelpRequested || len(missingRequired) > 0 || (payloadRaw == "" && len(schema.Parameters) > 0)

	if !shouldDisplayHelp {
		return false, nil
	}

	fmt.Println()
	fmt.Printf("📌 Service Overview: %s\n", schema.Name)
	if schema.Description != "" {
		fmt.Printf("   Description : %s\n", schema.Description)
	}
	if schema.Type != "" {
		fmt.Printf("   Service Type: %s\n", schema.Type)
	}
	fmt.Println()

	if len(missingRequired) > 0 {
		fmt.Printf("⚠️ Missing Required Parameter(s): %s\n\n", strings.Join(missingRequired, ", "))
	}

	if len(schema.Parameters) > 0 {
		fmt.Println("📋 Parameters Specification:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tTYPE\tREQUIRED\tDEFAULT\tDESCRIPTION")

		for _, k := range paramKeys {
			p := schema.Parameters[k]
			reqStr := "No"
			if p.Required {
				reqStr = "Yes"
			}
			defStr := p.Default
			if defStr == "" {
				defStr = "-"
			}
			pType := p.Type
			if pType == "" {
				pType = "string"
			}
			desc := fmt.Sprintf("Value for %s", k)
			if len(p.Options) > 0 {
				desc = fmt.Sprintf("Options: [%s]", strings.Join(p.Options, ", "))
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k, pType, reqStr, defStr, desc)
		}
		_ = w.Flush()
		fmt.Println()
	} else {
		fmt.Println("ℹ️ This service requires no payload parameters.")
		fmt.Println()
	}

	sampleKV := BuildSampleKVInputs(schema)

	fmt.Println("💡 Usage Example:")
	if sampleKV != "" {
		fmt.Printf("   proxyma service run --name %s --inputs \"%s\"\n\n", schema.Name, sampleKV)
	} else {
		fmt.Printf("   proxyma service run --name %s\n\n", schema.Name)
	}

	if isHelpRequested {
		return true, nil
	}

	if len(missingRequired) > 0 {
		return true, fmt.Errorf("missing required parameter(s) for service '%s': [%s]", schema.Name, strings.Join(missingRequired, ", "))
	}

	return true, nil
}
