package server

import (
	"net/http"
	"time"

	"mudp/internal/dockerx"
)

// HardwareSnapshot is the payload returned by GET /api/hardware. It bundles the
// host system facts (already gathered by the dashboard endpoint), a host-wide
// resource utilisation snapshot (CPU%, memory, load, temperature), and a per-GPU
// breakdown. All metric fields are best-effort: on hosts without GPUs the GPUs
// slice is empty, and on non-Linux hosts the CPU/temp fields are zero.
type HardwareSnapshot struct {
	System  dockerx.SystemInfo  `json:"system"`
	Host    dockerx.HostMetrics `json:"host"`
	GPUs    []dockerx.GPUCard   `json:"gpus"`
	Updated int64               `json:"updated"`
}

// hardwareSnapshot returns a host-wide hardware + GPU snapshot for the monitoring
// page. Visible to all activated users (the page is shared; per-user resource
// history lives under /api/resources/history).
func (a *App) hardwareSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sys := a.docker.SystemInfo(ctx)
	host := a.docker.HostMetrics(ctx)
	gpus := a.docker.GPUList(ctx)
	if gpus == nil {
		gpus = []dockerx.GPUCard{}
	}
	writeJSON(w, http.StatusOK, HardwareSnapshot{
		System:  sys,
		Host:    host,
		GPUs:    gpus,
		Updated: time.Now().UnixMilli(),
	})
}

// hardwareGPUList returns just the per-GPU snapshot, used by the create-container
// modal to render the GPU dropdown with the actual number of GPUs on the host.
func (a *App) hardwareGPUList(w http.ResponseWriter, r *http.Request) {
	gpus := a.docker.GPUList(r.Context())
	if gpus == nil {
		gpus = []dockerx.GPUCard{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"gpus":  gpus,
		"count": len(gpus),
	})
}
