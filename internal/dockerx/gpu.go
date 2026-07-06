package dockerx

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// gpuCache amortises the cost of shelling out to nvidia-smi. On hosts without
// NVIDIA drivers the binary is absent, and spawning a process per container
// (ListContainers calls GPUUsage once per GPU-bearing container) produces noise
// and latency. We cache both the binary's availability and its raw output for
// a short window so a single ListContainers call reuses one snapshot.
const (
	gpuSnapshotTTL     = 3 * time.Second
	gpuAvailabilityTTL = 30 * time.Second
)

type gpuSnapshot struct {
	at      time.Time
	metrics []gpuMetric
}

type gpuState struct {
	mu         sync.Mutex
	snapshot   gpuSnapshot
	availAt    time.Time
	availKnown bool
	avail      bool
}

var gpuCache = &gpuState{}

// nvidiaSmiAvailable reports whether nvidia-smi exists and runs successfully,
// caching the probe result so we don't fork on every container listing.
func nvidiaSmiAvailable() bool {
	gpuCache.mu.Lock()
	defer gpuCache.mu.Unlock()
	now := time.Now()
	if gpuCache.availKnown && now.Sub(gpuCache.availAt) < gpuAvailabilityTTL {
		return gpuCache.avail
	}
	_, err := exec.LookPath("nvidia-smi")
	gpuCache.avail = err == nil
	gpuCache.availKnown = true
	gpuCache.availAt = now
	return gpuCache.avail
}

// queryGPU returns the parsed GPU metrics, reusing a recent snapshot when one
// is still fresh. This is what backs GPUUsage across many containers.
func queryGPU(ctx context.Context) []gpuMetric {
	gpuCache.mu.Lock()
	now := time.Now()
	if now.Sub(gpuCache.snapshot.at) < gpuSnapshotTTL {
		m := gpuCache.snapshot.metrics
		gpuCache.mu.Unlock()
		return m
	}
	gpuCache.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Query a richer set of fields so the hardware monitoring page can show
	// temperature, power draw, and memory utilisation in addition to the basic
	// GPU% + memory figures the dashboard/usage views already use. All fields are
	// best-effort: missing/unavailable values come back as empty strings in the
	// CSV and parse to zero.
	out, err := exec.CommandContext(cctx, "nvidia-smi",
		"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,utilization.memory",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	metrics := parseGPUMetrics(string(out))

	gpuCache.mu.Lock()
	gpuCache.snapshot = gpuSnapshot{at: time.Now(), metrics: metrics}
	gpuCache.mu.Unlock()
	return metrics
}
