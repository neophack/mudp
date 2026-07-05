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
		Stats struct {
			Cache uint64 `json:"cache"`
		} `json:"stats"`
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

	// Memory: usage minus cache (page cache) for the working set. Guard against
	// Docker reporting more cache than total usage, which would underflow uint64.
	usage := r.MemoryStats.Usage
	if r.MemoryStats.Stats.Cache > usage {
		usage = 0
	} else {
		usage -= r.MemoryStats.Stats.Cache
	}
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
