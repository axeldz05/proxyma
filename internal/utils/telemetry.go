package utils

import (
	"os"
	"strconv"
	"strings"
)

// ReadMemoryLimit returns the memory limit in bytes from cgroups, or 0 if unlimited/error.
func ReadMemoryLimit() int64 {
	// Try cgroups v2
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		valStr := strings.TrimSpace(string(data))
		if valStr != "max" && valStr != "" {
			if val, err := strconv.ParseInt(valStr, 10, 64); err == nil {
				return val
			}
		}
	}
	// Try cgroups v1
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		valStr := strings.TrimSpace(string(data))
		if val, err := strconv.ParseInt(valStr, 10, 64); err == nil {
			// Some systems return a huge number (like 9223372036854771712 or 18446744073709551615) when there's no limit
			if val > 0 && val < 9223372036854770000 {
				return val
			}
		}
	}
	return 0
}

// ReadCPULimit returns the CPU limit in cores from cgroups, or 0 if unlimited/error.
func ReadCPULimit() float64 {
	// Try cgroups v2
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) == 2 && parts[0] != "max" {
			quota, errQ := strconv.ParseFloat(parts[0], 64)
			period, errP := strconv.ParseFloat(parts[1], 64)
			if errQ == nil && errP == nil && period > 0 {
				return quota / period
			}
		}
	}
	// Try cgroups v1
	quotaData, errQ := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	periodData, errP := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if errQ == nil && errP == nil {
		quotaStr := strings.TrimSpace(string(quotaData))
		periodStr := strings.TrimSpace(string(periodData))
		quota, errQVal := strconv.ParseFloat(quotaStr, 64)
		period, errPVal := strconv.ParseFloat(periodStr, 64)
		if errQVal == nil && errPVal == nil && quota > 0 && period > 0 {
			return quota / period
		}
	}
	return 0
}
