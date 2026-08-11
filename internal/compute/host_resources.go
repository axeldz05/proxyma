package compute

import (
	"proxyma/internal/utils"
	"runtime"
	"sync"
)

// HostResourceSampler returns relative CPU load and memory pressure (0 = idle; values may exceed 1 under stress).
type HostResourceSampler func() (cpuLoad, memPressure float64)

// The sampler is a process-wide seam: bids read it from request goroutines while a
// test restores it from cleanup, so both sides go through the lock.
var (
	samplerMu           sync.RWMutex
	hostResourceSampler HostResourceSampler = defaultHostResourceSampler
)

func currentHostResourceSampler() HostResourceSampler {
	samplerMu.RLock()
	defer samplerMu.RUnlock()
	return hostResourceSampler
}

func setHostResourceSampler(fn HostResourceSampler) {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	if fn == nil {
		hostResourceSampler = defaultHostResourceSampler
		return
	}
	hostResourceSampler = fn
}

// SetHostResourceSampler replaces the host resource sampler (tests). Restores previous on cleanup.
func SetHostResourceSampler(fn HostResourceSampler) (restore func()) {
	samplerMu.RLock()
	prev := hostResourceSampler
	samplerMu.RUnlock()

	setHostResourceSampler(fn)
	return func() { setHostResourceSampler(prev) }
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
