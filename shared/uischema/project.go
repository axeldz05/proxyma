package uischema

import (
	"fmt"

	"proxyma/internal/protocol"
)

// FormatBytes renders a byte count with base-1024 units (SSOT for CLI/bind presentation).
func FormatBytes(bytesVal int64) string {
	if bytesVal <= 0 {
		return "0 B"
	}
	if bytesVal >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytesVal)/(1024*1024*1024))
	} else if bytesVal >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytesVal)/(1024*1024))
	} else if bytesVal >= 1024 {
		return fmt.Sprintf("%.2f KB", float64(bytesVal)/1024)
	}
	return fmt.Sprintf("%d B", bytesVal)
}

// FormatRate formats a bytes-per-second value.
func FormatRate(bps float64) string {
	return FormatBytes(int64(bps)) + "/s"
}

// BandwidthStatsRows projects raw bandwidth stats into metric/value rows matching Registry Columns.
func BandwidthStatsRows(stats protocol.BandwidthStats) []map[string]any {
	return []map[string]any{
		{"metric": "Download Speed", "value": FormatRate(float64(stats.DownloadSpeed))},
		{"metric": "Upload Speed", "value": FormatRate(float64(stats.UploadSpeed))},
		{"metric": "Total Received", "value": FormatBytes(stats.TotalReceived)},
		{"metric": "Total Sent", "value": FormatBytes(stats.TotalSent)},
	}
}

// ProjectRows formats table rows according to column FieldSelector / Format (SSOT for multi-UI).
func ProjectRows(columns []TableColumn, rows []map[string]any) [][]string {
	out := make([][]string, 0, len(rows))
	for _, item := range rows {
		rowFields := make([]string, 0, len(columns))
		for _, col := range columns {
			rowFields = append(rowFields, projectCell(col, item))
		}
		out = append(out, rowFields)
	}
	return out
}

func projectCell(col TableColumn, item map[string]any) string {
	if col.FieldSelector == "." {
		return fmt.Sprintf("%v", item)
	}
	val := item[col.FieldSelector]
	if val == nil {
		val = ""
	}
	formatted := ""
	if slice, ok := val.([]any); ok {
		formatted = fmt.Sprintf("%d", len(slice))
	} else {
		formatted = fmt.Sprintf("%v", val)
	}

	switch col.Format {
	case "bytes":
		var bytesVal int64
		switch v := val.(type) {
		case float64:
			bytesVal = int64(v)
		case int64:
			bytesVal = v
		case int:
			bytesVal = int64(v)
		}
		return FormatBytes(bytesVal)
	case "boolean":
		if bv, ok := val.(bool); ok {
			return fmt.Sprintf("%t", bv)
		}
	case "status":
		switch col.FieldSelector {
		case "deleted":
			if bv, ok := val.(bool); ok && bv {
				return "Deleted"
			}
			return "Active"
		case "online":
			if bv, ok := val.(bool); ok && bv {
				return "ONLINE"
			}
			return "OFFLINE"
		}
	}
	return formatted
}
