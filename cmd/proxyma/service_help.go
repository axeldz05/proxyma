package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
)

// GetServiceSchemaLocal fetches the ServiceSchema for a target service from the daemon or local registry.
func GetServiceSchemaLocal(storagePath string, serviceName string) (*protocol.ServiceSchema, error) {
	proxyma_bind.SetStoragePath(storagePath)
	schemaJSON := proxyma_bind.GetServiceSchema(serviceName)

	if !strings.Contains(schemaJSON, `"error":`) {
		var schema protocol.ServiceSchema
		if err := json.Unmarshal([]byte(schemaJSON), &schema); err == nil {
			if schema.Name == "" {
				schema.Name = serviceName
			}
			return &schema, nil
		}
	}

	// Offline fallback via shared L1 loader (no ad-hoc parse duplication).
	svcs, err := compute.LoadServicesMap(storagePath)
	if err == nil {
		if svc, ok := svcs[serviceName]; ok {
			s := svc.Schema
			if s.Name == "" {
				s.Name = serviceName
			}
			if s.Type == "" {
				s.Type = svc.Type
			}
			return &s, nil
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

// sampleValue synthesizes a representative sample for a parameter (L1).
func sampleValue(paramName string, param protocol.ServiceParameter) any {
	if param.Default != "" {
		switch param.Type {
		case "bool":
			return param.Default == "true" || param.Default == "1"
		case "int":
			var val int
			_, _ = fmt.Sscanf(param.Default, "%d", &val)
			return val
		default:
			return param.Default
		}
	}
	lowerKey := strings.ToLower(paramName)
	switch param.Type {
	case "bool":
		return true
	case "int":
		return 100
	case "file":
		return "/path/to/input_file"
	default:
		if strings.Contains(lowerKey, "path") || strings.Contains(lowerKey, "file") || strings.Contains(lowerKey, "doc") {
			return "/path/to/input_file"
		}
		if strings.Contains(lowerKey, "lang") {
			return "eng"
		}
		if strings.Contains(lowerKey, "user") || strings.Contains(lowerKey, "name") {
			return "user_example"
		}
		if len(param.Options) > 0 {
			return param.Options[0]
		}
		return "example_value"
	}
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
		val := sampleValue(k, schema.Parameters[k])
		kvPairs = append(kvPairs, fmt.Sprintf("%s=%v", k, val))
	}
	return strings.Join(kvPairs, ",")
}

// BuildSampleJSONPayload constructs a representative JSON sample based on parameter definitions.
func BuildSampleJSONPayload(schema *protocol.ServiceSchema) string {
	sample := make(map[string]any)
	keys := make([]string, 0, len(schema.Parameters))
	for k := range schema.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sample[k] = sampleValue(k, schema.Parameters[k])
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
