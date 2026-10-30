package utils

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	cgroupMu                sync.RWMutex
	cgroupV2CPUMaxPath      = "/sys/fs/cgroup/cpu.max"
	cgroupV1CFSQuotaPath    = "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"
	cgroupV1CFSPeriodPath   = "/sys/fs/cgroup/cpu/cpu.cfs_period_us"
	cgroupV2MemoryMaxPath   = "/sys/fs/cgroup/memory.max"
	cgroupV1MemoryLimitPath = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

// SetCgroupPathsForTest sets custom paths for cgroup files for testing purposes and returns a restore function.
func SetCgroupPathsForTest(v2CPU, v1Quota, v1Period, v2Mem, v1Mem string) func() {
	cgroupMu.Lock()
	prevV2CPU := cgroupV2CPUMaxPath
	prevV1Q := cgroupV1CFSQuotaPath
	prevV1P := cgroupV1CFSPeriodPath
	prevV2M := cgroupV2MemoryMaxPath
	prevV1M := cgroupV1MemoryLimitPath

	if v2CPU != "" {
		cgroupV2CPUMaxPath = v2CPU
	}
	if v1Quota != "" {
		cgroupV1CFSQuotaPath = v1Quota
	}
	if v1Period != "" {
		cgroupV1CFSPeriodPath = v1Period
	}
	if v2Mem != "" {
		cgroupV2MemoryMaxPath = v2Mem
	}
	if v1Mem != "" {
		cgroupV1MemoryLimitPath = v1Mem
	}
	cgroupMu.Unlock()

	return func() {
		cgroupMu.Lock()
		cgroupV2CPUMaxPath = prevV2CPU
		cgroupV1CFSQuotaPath = prevV1Q
		cgroupV1CFSPeriodPath = prevV1P
		cgroupV2MemoryMaxPath = prevV2M
		cgroupV1MemoryLimitPath = prevV1M
		cgroupMu.Unlock()
	}
}

// ReadMemoryLimit returns the memory limit in bytes from cgroups, or 0 if unlimited/error.
func ReadMemoryLimit() int64 {
	cgroupMu.RLock()
	v2Path := cgroupV2MemoryMaxPath
	v1Path := cgroupV1MemoryLimitPath
	cgroupMu.RUnlock()

	// Try cgroups v2
	if data, err := os.ReadFile(v2Path); err == nil {
		valStr := strings.TrimSpace(string(data))
		if valStr != "max" && valStr != "" {
			if val, err := strconv.ParseInt(valStr, 10, 64); err == nil {
				return val
			}
		}
	}
	// Try cgroups v1
	if data, err := os.ReadFile(v1Path); err == nil {
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
	cgroupMu.RLock()
	v2Path := cgroupV2CPUMaxPath
	v1QuotaPath := cgroupV1CFSQuotaPath
	v1PeriodPath := cgroupV1CFSPeriodPath
	cgroupMu.RUnlock()

	// Try cgroups v2
	if data, err := os.ReadFile(v2Path); err == nil {
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
	quotaData, errQ := os.ReadFile(v1QuotaPath)
	periodData, errP := os.ReadFile(v1PeriodPath)
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
