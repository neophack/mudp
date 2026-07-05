package dockerx

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	volumetypes "github.com/docker/docker/api/types/volume"
)

// SystemInfo is the host/environment snapshot shown on the dashboard.
type SystemInfo struct {
	Name       string         `json:"name"`
	OSType     string         `json:"osType"`
	OSVersion  string         `json:"osVersion"`
	Kernel     string         `json:"kernel"`
	Arch       string         `json:"arch"`
	CPUs       int            `json:"cpus"`
	MemoryGB   float64        `json:"memoryGb"`
	DockerVer  string         `json:"dockerVersion"`
	APIVersion string         `json:"apiVersion"`
	StorageDrv string         `json:"storageDriver"`
	ServerTime int64          `json:"serverTime"`
	Containers ContainerStats `json:"containers"`
	Images     ResourceStats  `json:"images"`
	Volumes    ResourceStats  `json:"volumes"`
	Networks   int            `json:"networks"`
	Healthy    bool           `json:"healthy"`
	HealthyMsg string         `json:"healthyMsg,omitempty"`
	AgentCPU   int            `json:"agentCpu"`
	AgentMemMB float64        `json:"agentMemMb"`
	AgentGoRt  string         `json:"agentGoRuntime"`
}

// ContainerStats breaks container counts down by lifecycle state.
type ContainerStats struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Stopped   int `json:"stopped"`
	Paused    int `json:"paused"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
}

// ResourceStats counts a Docker resource kind plus its on-disk footprint.
type ResourceStats struct {
	Count  int     `json:"count"`
	SizeB  int64   `json:"sizeBytes"`
	SizeMB float64 `json:"sizeMb"`
}

// SystemInfo gathers the environment snapshot used by the dashboard. Every
// sub-query is best-effort: a missing piece never fails the whole call so a
// partially-reachable daemon still renders a usable dashboard.
func (d *Client) SystemInfo(ctx context.Context) SystemInfo {
	out := SystemInfo{
		Name:       hostname(),
		AgentGoRt:  runtime.Version(),
		ServerTime: time.Now().Unix(),
		AgentCPU:   runtime.NumCPU(),
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out.AgentMemMB = round2(float64(m.Alloc) / 1024 / 1024)

	info, err := d.c.Info(ctx)
	if err != nil {
		out.HealthyMsg = fmt.Sprintf("Docker unreachable: %v", err)
		return out
	}
	out.Healthy = true
	out.OSType = info.OSType
	out.OSVersion = info.OperatingSystem
	out.Kernel = info.KernelVersion
	out.Arch = info.Architecture
	out.CPUs = info.NCPU
	out.MemoryGB = round2(float64(info.MemTotal) / 1024 / 1024 / 1024)
	out.StorageDrv = info.Driver
	// Container counts are computed from the mudp-managed list below (filtered),
	// so the dashboard reflects what users actually own rather than the host's
	// full container inventory (which includes other tools' containers).

	if ver, err := d.c.ServerVersion(ctx); err == nil {
		out.DockerVer = ver.Version
		out.APIVersion = ver.APIVersion
	}

	// Images: only mudp-published images (tagged with the mudp prefix).
	if imgs, err := d.c.ImageList(ctx, image.ListOptions{}); err == nil {
		var size int64
		count := 0
		for _, im := range imgs {
			managed := false
			for _, tag := range im.RepoTags {
				if strings.HasPrefix(tag, Prefix) && !strings.Contains(tag, "<none>") {
					managed = true
					break
				}
			}
			if managed {
				count++
				size += im.Size
			}
		}
		out.Images = ResourceStats{Count: count, SizeB: size, SizeMB: round2(float64(size) / 1024 / 1024)}
	}

	// Volumes: only mudp-managed volumes (label mudp.managed=true).
	if dv, err := d.c.VolumeList(ctx, volumetypes.ListOptions{Filters: managedVolumeFilter()}); err == nil {
		var size int64
		for _, v := range dv.Volumes {
			if v.UsageData != nil {
				size += v.UsageData.Size
			}
		}
		out.Volumes = ResourceStats{Count: len(dv.Volumes), SizeB: size, SizeMB: round2(float64(size) / 1024 / 1024)}
	}

	// Networks: count every network the user would see in the Networks view —
	// mudp-managed networks plus Docker's built-in defaults (bridge, host, none).
	// This keeps the dashboard tile consistent with the Networks page.
	if nets, err := d.c.NetworkList(ctx, network.ListOptions{}); err == nil {
		count := 0
		for _, n := range nets {
			if n.Labels[ManagedLabel] == "true" || isSystemNetworkName(n.Name) {
				count++
			}
		}
		out.Networks = count
	}

	// Containers: mudp-managed only, broken down by lifecycle state.
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: managedLabelFilter()}); err == nil {
		cs := ContainerStats{Total: len(list)}
		for _, c := range list {
			switch c.State {
			case "running":
				cs.Running++
			case "paused":
				cs.Paused++
			case "exited", "dead", "created":
				cs.Stopped++
			}
		}
		out.Containers = cs
	}

	// Health rollup needs per-container listing.
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: healthFilter("healthy")}); err == nil {
		out.Containers.Healthy = len(list)
	}
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: healthFilter("unhealthy")}); err == nil {
		out.Containers.Unhealthy = len(list)
	}
	return out
}

// DockerPing reports whether the engine responds. Used by health endpoints.
func (d *Client) DockerPing(ctx context.Context) error {
	_, err := d.c.Ping(ctx)
	return err
}

func healthFilter(state string) filters.Args {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	args.Add("health", state)
	return args
}

// managedLabelFilter matches only mudp-managed resources (label
// mudp.managed=true). Used to keep dashboard counts scoped to what mudp owns.
func managedLabelFilter() filters.Args {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	return args
}

// managedVolumeFilter is the volume-list equivalent of managedLabelFilter.
func managedVolumeFilter() filters.Args {
	return managedLabelFilter()
}

// round2 rounds to two decimals using a small epsilon to absorb binary
// float representation drift (e.g. 1.005 stored as 1.00499999...).
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v >= float64(math.MaxInt64)/100 {
		return float64(math.MaxInt64) / 100
	}
	if v <= float64(math.MinInt64)/100 {
		return float64(math.MinInt64) / 100
	}
	return float64(int(v*100+0.5+1e-9)) / 100
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "docker-host"
}
