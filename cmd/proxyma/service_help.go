package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/protocol"
	"proxyma/shared/uischema"
)

// lookupServiceSchema sets storage and returns a ServiceSchema pointer.
func lookupServiceSchema(storagePath string, serviceName string) (*protocol.ServiceSchema, error) {
	proxyma_bind.SetStoragePath(storagePath)
	schema, err := proxyma_bind.LookupServiceSchema(serviceName)
	if err != nil {
		return nil, err
	}
	return &schema, nil
}

// ParseInputsToJSON is a thin CLI alias for uischema.NormalizePayloadJSON (SSOT).
func ParseInputsToJSON(inputsRaw string) string {
	return uischema.NormalizePayloadJSON(inputsRaw)
}

// sampleValue synthesizes a representative sample for a parameter (L1).
// Uses Default, Options, Type, and UIHint only — no parameter-name sniffing.
func sampleValue(paramName string, param protocol.ServiceParameter) any {
	return param.CoerceDefault(paramName)
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

	schema, err := lookupServiceSchema(storagePath, serviceName)
	if err != nil || schema == nil {
		return false, nil
	}

	var missingRequired []string
	paramKeys := make([]string, 0, len(schema.Parameters))
	for k := range schema.Parameters {
		paramKeys = append(paramKeys, k)
	}
	sort.Strings(paramKeys)

	missingRequired = protocol.MissingRequired(*schema, parsedPayload)

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
				pType = protocol.ParamTypeString
			}
			desc, _ := protocol.DescribeParameter(k, p)
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
