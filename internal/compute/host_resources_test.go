package compute

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"proxyma/internal/utils"

	"github.com/stretchr/testify/require"
)

func TestDefaultHostResourceSampler(t *testing.T) {
	cpuLoad, memPressure := defaultHostResourceSampler()
	require.GreaterOrEqual(t, cpuLoad, 0.0)
	require.LessOrEqual(t, cpuLoad, 10.0)
	require.GreaterOrEqual(t, memPressure, 0.0)
	require.LessOrEqual(t, memPressure, 10.0)
}

func TestSetHostResourceSampler(t *testing.T) {
	restore := SetHostResourceSampler(func() (float64, float64) {
		return 0.75, 0.5
	})
	defer restore()

	cpu, mem := currentHostResourceSampler()()
	require.InDelta(t, 0.75, cpu, 0.001)
	require.InDelta(t, 0.5, mem, 0.001)

	// Restoring should return to previous
	restore()
	cpu, mem = currentHostResourceSampler()()
	require.GreaterOrEqual(t, cpu, 0.0)
	require.LessOrEqual(t, cpu, 10.0)

	// Passing nil should reset to defaultHostResourceSampler
	restoreNil := SetHostResourceSampler(nil)
	defer restoreNil()
	cpuNil, memNil := currentHostResourceSampler()()
	require.GreaterOrEqual(t, cpuNil, 0.0)
	require.LessOrEqual(t, cpuNil, 10.0)
	require.GreaterOrEqual(t, memNil, 0.0)
	require.LessOrEqual(t, memNil, 10.0)
}

func TestHostResourceBoundaryClamping(t *testing.T) {
	procs := runtime.GOMAXPROCS(0)
	if procs < 1 {
		procs = 1
	}

	// Spawn enough idle goroutines to exceed procs*50*10
	needed := procs * 50 * 11
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < needed; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-stop
		}()
	}

	cpu, _ := defaultHostResourceSampler()
	close(stop)
	wg.Wait()

	require.Equal(t, 10.0, cpu, "defaultHostResourceSampler must clamp cpuLoad to 10.0 when load exceeds 10")
}

func TestHostResourceCgroupsQuotaReading(t *testing.T) {
	tempDir := t.TempDir()
	v2CPU := filepath.Join(tempDir, "cpu.max")
	v2Mem := filepath.Join(tempDir, "memory.max")

	// Set quota: 50000us / 100000us = 0.5 cores
	require.NoError(t, os.WriteFile(v2CPU, []byte("50000 100000\n"), 0644))
	// Set memory limit: 256MB
	require.NoError(t, os.WriteFile(v2Mem, []byte("268435456\n"), 0644))

	restore := utils.SetCgroupPathsForTest(v2CPU, "", "", v2Mem, "")
	defer restore()

	limit := utils.ReadCPULimit()
	require.InDelta(t, 0.5, limit, 0.001)

	memLimit := utils.ReadMemoryLimit()
	require.Equal(t, int64(268435456), memLimit)

	// Test defaultHostResourceSampler with active cgroup limits
	cpu, mem := defaultHostResourceSampler()
	require.GreaterOrEqual(t, cpu, 0.0)
	require.LessOrEqual(t, cpu, 10.0)
	require.GreaterOrEqual(t, mem, 0.0)
	require.LessOrEqual(t, mem, 10.0)

	// Test severe quota limitation driving cpuLoad to clamping limit (10.0)
	require.NoError(t, os.WriteFile(v2CPU, []byte("1 1000000\n"), 0644)) // 0.000001 cores
	clampedCPU, _ := defaultHostResourceSampler()
	require.Equal(t, 10.0, clampedCPU, "severe cgroup quota must clamp cpuLoad to 10.0")

	// Test severe memory limit driving memPressure to clamping limit (10.0)
	require.NoError(t, os.WriteFile(v2Mem, []byte("1\n"), 0644)) // 1 byte limit
	_, clampedMem := defaultHostResourceSampler()
	require.Equal(t, 10.0, clampedMem, "severe cgroup memory limit must clamp memPressure to 10.0")
}

func TestConcurrentHostResourceSampler(t *testing.T) {
	defer setHostResourceSampler(defaultHostResourceSampler)

	var wg sync.WaitGroup
	workers := 20
	iterations := 100

	for i := 0; i < workers; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if workerID%2 == 0 {
					cpu, mem := currentHostResourceSampler()()
					require.GreaterOrEqual(t, cpu, 0.0)
					require.GreaterOrEqual(t, mem, 0.0)
				} else {
					restore := SetHostResourceSampler(func() (float64, float64) {
						return float64(workerID), float64(j)
					})
					cpu, _ := currentHostResourceSampler()()
					require.GreaterOrEqual(t, cpu, 0.0)
					restore()
				}
			}
		}()
	}
	wg.Wait()
}
