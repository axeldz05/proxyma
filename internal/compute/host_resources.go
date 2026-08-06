package compute

import (
	"proxyma/internal/utils"
	"runtime"
)

// HostResourceSampler returns relative CPU load and memory pressure (0 = idle; values may exceed 1 under stress).
type HostResourceSampler func() (cpuLoad, memPressure float64)

var hostResourceSampler HostResourceSampler = defaultHostResourceSampler

// SetHostResourceSampler replaces the host resource sampler (tests). Restores previous on cleanup.
func SetHostResourceSampler(fn HostResourceSampler) (restore func()) {
	prev := hostResourceSampler
	if fn == nil {
		hostResourceSampler = defaultHostResourceSampler
	} else {
		hostResourceSampler = fn
	}
	return func() { hostResourceSampler = prev }
}

func defaultHostResourceSampler() (cpuLoad, memPressure float64) {
	procs := runtime.GOMAXPROCS(0)
	if procs < 1 {
		procs = 1
	}
	cpuLoad = float64(runtime.NumGoroutine()) / float64(procs*50)
	if cpuLoad > 10 {
		cpuLoad = 10
	}
	if cpuLimit := utils.ReadCPULimit(); cpuLimit > 0 && cpuLimit < float64(procs) {
		cpuLoad *= float64(procs) / cpuLimit
		if cpuLoad > 10 {
			cpuLoad = 10
		}
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if memLimit := utils.ReadMemoryLimit(); memLimit > 0 {
		memPressure = float64(ms.HeapAlloc) / float64(memLimit)
	} else {
		memPressure = float64(ms.HeapAlloc) / float64(512<<20)
	}
	if memPressure > 10 {
		memPressure = 10
	}
	return cpuLoad, memPressure
}
