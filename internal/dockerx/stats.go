package dockerx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// StatsSample is one point-in-time resource reading for a container.
type StatsSample struct {
	Timestamp      int64   `json:"ts"`
	CPUPercent     float64 `json:"cpuPct"`
	MemoryMB       float64 `json:"memMb"`
	MemoryLimitMB  float64 `json:"memLimitMb"`
	MemoryPct      float64 `json:"memPct"`
	NetRxKB        float64 `json:"netRxKb"`
	NetTxKB        float64 `json:"netTxKb"`
	BlockReadKB    float64 `json:"blockReadKb"`
	BlockWriteKB   float64 `json:"blockWriteKb"`
	PIDs           uint64  `json:"pids"`
	GPUPercent     float64 `json:"gpuPct,omitempty"`
	GPUMemoryMB    float64 `json:"gpuMemMb,omitempty"`
	GPUMemoryLimit float64 `json:"gpuMemLimitMb,omitempty"`
	GPUMemoryPct   float64 `json:"gpuMemPct,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// rawStats is the subset of Docker's stats JSON we decode for sampling.
type rawStats struct {
	Read        time.Time `json:"read"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"precpu_stats"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
		Stats memoryStatsDetail `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// memoryStatsDetail mirrors the memory_stats.stats object Docker reports.
// Field names are the raw cgroup keys (cgroup v1 or v2 translated by the
// Docker daemon). Every field is optional; absent keys decode to zero.
type memoryStatsDetail struct {
	Cache             uint64 `json:"cache"`
	RSS               uint64 `json:"rss"`                     // resident anonymous memory (v1)
	Rss               uint64 `json:"Rss"`                     // cgroup v2 capitalised variant
	ActiveAnon        uint64 `json:"active_anon"`             // v1: active anonymous pages
	InactiveFile      uint64 `json:"inactive_file"`           // v1: reclaimable file pages
	TotalInactiveFile uint64 `json:"total_inactive_file"`     // v2 unified key
}

// SampleStats takes a single one-shot stats reading for a container.
func (d *Client) SampleStats(ctx context.Context, id string) (StatsSample, error) {
	return d.SampleStatsWithGPU(ctx, id, "")
}

// SampleStatsWithGPU is like SampleStats but also enriches the sample with the
// container's GPU utilization when a non-empty GPU spec (e.g. "0", "all") is
// supplied. The spec mirrors the mudp.gpu container label.
func (d *Client) SampleStatsWithGPU(ctx context.Context, id, gpuSpec string) (StatsSample, error) {
	resp, err := d.c.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return StatsSample{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return StatsSample{}, err
	}
	s := parseStats(body)
	if gpuSpec != "" && gpuSpec != "none" {
		if gpu, err := d.GPUUsage(ctx, gpuSpec); err == nil && (gpu.MemoryTotalMB > 0 || gpu.Percent > 0) {
			s.GPUPercent = gpu.Percent
			s.GPUMemoryMB = gpu.MemoryMB
			s.GPUMemoryLimit = gpu.MemoryTotalMB
			s.GPUMemoryPct = gpu.MemoryPct
		}
	}
	return s, nil
}

// StreamStats samples the container every interval until ctx is cancelled,
// sending each sample to ch. Designed to back an SSE endpoint. When gpuSpec is
// non-empty, each sample is enriched with live GPU utilization.
func (d *Client) StreamStats(ctx context.Context, id, gpuSpec string, interval time.Duration, ch chan<- StatsSample) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		sample, err := d.SampleStatsWithGPU(ctx, id, gpuSpec)
		if err != nil {
			sample.Error = err.Error()
		}
		select {
		case ch <- sample:
		case <-ctx.Done():
			return
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// parseStats turns the Docker stats JSON into a StatsSample with derived rates.
func parseStats(body []byte) StatsSample {
	var r rawStats
	if err := json.Unmarshal(body, &r); err != nil {
		return StatsSample{Error: fmt.Sprintf("decode: %v", err)}
	}
	s := StatsSample{Timestamp: time.Now().Unix()}

	// CPU%: delta of total_usage / (delta of system_usage * online_cpus) * 100.
	cpuDelta := float64(r.CPUStats.CPUUsage.TotalUsage) - float64(r.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(r.CPUStats.SystemCPUUsage) - float64(r.PreCPUStats.SystemCPUUsage)
	cpus := uint32(1)
	if r.CPUStats.OnlineCPUs > 0 {
		cpus = r.CPUStats.OnlineCPUs
	}
	if sysDelta > 0 && cpuDelta > 0 {
		s.CPUPercent = round2((cpuDelta / sysDelta) * float64(cpus) * 100)
	}

	// Memory working set. Docker's raw `usage` includes page cache (file-backed
	// memory the kernel can reclaim under pressure), so subtracting it gives a
	// value that matches `docker stats`. The exact subtrahend depends on the
	// cgroup driver:
	//
	//   cgroup v1: usage - total_inactive_file (or inactive_file)
	//   cgroup v2: usage - inactive_file  (the unified-hierarchy key)
	//
	// Older code subtracted the whole `cache` field, which (a) is usually 0 on
	// v2 so memory was reported far too high, and (b) on v1 still included the
	// *active* file pages that can't be reclaimed. See workingSet for details.
	usage := workingSet(r.MemoryStats.Usage, r.MemoryStats.Stats)
	s.MemoryMB = round2(float64(usage) / 1024 / 1024)
	s.MemoryLimitMB = round2(float64(r.MemoryStats.Limit) / 1024 / 1024)
	if s.MemoryLimitMB > 0 {
		s.MemoryPct = round2(s.MemoryMB / s.MemoryLimitMB * 100)
	}

	// Network: sum across interfaces.
	var rx, tx uint64
	for _, n := range r.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	s.NetRxKB = round2(float64(rx) / 1024)
	s.NetTxKB = round2(float64(tx) / 1024)

	// Block I/O.
	for _, b := range r.BlkioStats.IoServiceBytesRecursive {
		switch b.Op {
		case "Read":
			s.BlockReadKB = round2(float64(b.Value) / 1024)
		case "Write":
			s.BlockWriteKB = round2(float64(b.Value) / 1024)
		}
	}
	s.PIDs = r.PidsStats.Current
	return s
}

// workingSet derives the container's resident memory (what `docker stats` shows)
// from raw usage minus reclaimable file cache. The kernel can drop inactive file
// pages under memory pressure, so those don't count against the working set.
//
// We prefer the unified `total_inactive_file`/`inactive_file` cgroup keys (this
// is what cAdvisor uses); on older v1 setups we fall back to those same keys
// and finally to plain `cache` so behaviour never regresses worse than before.
// If the daemon reports a subtrahend larger than usage (seen transiently during
// cgroup accounting), we clamp to zero rather than underflow.
func workingSet(usage uint64, d memoryStatsDetail) uint64 {
	sub := d.TotalInactiveFile
	if sub == 0 {
		sub = d.InactiveFile
	}
	if sub == 0 {
		// Legacy fallback only: cache is a coarse v1-era approximation.
		sub = d.Cache
	}
	if sub >= usage {
		return 0
	}
	return usage - sub
}
