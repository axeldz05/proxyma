package uischema

import (
	"encoding/json"
	"strconv"
	"strings"
)

// NormalizePayloadJSON converts key=value pairs or JSON into a JSON string (SSOT).
// Empty input yields "{}". Objects and arrays pass through. KV values are typed
// (bool / int / float / string) like the former CLI ParseInputsToJSON.
func NormalizePayloadJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return raw
	}

	payload := make(map[string]any)
	for _, pair := range strings.Split(raw, ",") {
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
		switch {
		case vStr == "true":
			payload[k] = true
		case vStr == "false":
			payload[k] = false
		case !strings.Contains(vStr, " "):
			if num, err := strconv.ParseFloat(vStr, 64); err == nil {
				if float64(int64(num)) == num {
					payload[k] = int64(num)
				} else {
					payload[k] = num
				}
				continue
			}
			fallthrough
		default:
			payload[k] = vStr
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
