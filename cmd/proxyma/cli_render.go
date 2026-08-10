package main

import "proxyma/shared/uischema"

func formatBytes(bytesVal int64) string {
	return uischema.FormatBytes(bytesVal)
}

func formatRate(bps float64) string {
	return uischema.FormatRate(bps)
}
