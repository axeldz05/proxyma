package main

import "fmt"

func formatBytes(bytesVal int64) string {
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

func formatRate(bps float64) string {
	return formatBytes(int64(bps)) + "/s"
}
