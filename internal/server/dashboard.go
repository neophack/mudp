package server

import (
	"net/http"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// usageRow is the per-user resource rollup shared by the dashboard and the
// standalone usage endpoint.
type usageRow struct {
	store.User
	Containers       int     `json:"containers"`
	MemoryMB         float64 `json:"memoryMb"`
	DiskMB           float64 `json:"diskMb"`
	GPU              string  `json:"gpu"`
	GPUPercent       float64 `json:"gpuPct"`
	GPUMemoryMB      float64 `json:"gpuMemMb"`
	GPUMemoryTotalMB float64 `json:"gpuMemTotalMb"`
	GPUMemoryPct     float64 `json:"gpuMemPct"`
}

// dashboardResponse aggregates everything the home screen renders in one round
// trip: environment info, the caller's container rollup, and (admin only) the
// per-user usage summary.
type dashboardResponse struct {
	System  dockerx.SystemInfo `json:"system"`
	Mine    mineRollup         `json:"mine"`
	Usage   []usageRow         `json:"usage,omitempty"`
	IsAdmin bool               `json:"isAdmin"`
}

type mineRollup struct {
	Containers int     `json:"containers"`
	Running    int     `json:"running"`
	MemoryMB   float64 `json:"memoryMb"`
	DiskMB     float64 `json:"diskMb"`
	Cap        int     `json:"cap"`
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	sys := a.docker.SystemInfo(r.Context())

	items, _ := a.docker.ListContainers(r.Context(), u.Username, u.Role == "admin")
	mine := mineRollup{Cap: u.ContainerCap}
	for _, c := range items {
		// Admins see everyone; only count the caller's own here.
		if u.Role != "admin" || c.Labels["mudp.user"] == u.Username {
			mine.Containers++
			if c.State == "running" {
				mine.Running++
			}
			mine.MemoryMB += c.MemoryMB
			mine.DiskMB += c.DiskMB
		}
	}

	resp := dashboardResponse{System: sys, Mine: mine, IsAdmin: u.Role == "admin"}
	if u.Role == "admin" {
		resp.Usage = buildUsage(r, a)
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildUsage computes the admin per-user usage table. Extracted so the
// dashboard and the standalone usage endpoint share one implementation.
func buildUsage(r *http.Request, a *App) []usageRow {
	users, err := a.db.Users()
	if err != nil {
		return nil
	}
	var out []usageRow
	for _, u := range users {
		items, _ := a.docker.ListContainers(r.Context(), u.Username, false)
		row := usageRow{User: u, Containers: len(items)}
		gpus := map[string]bool{}
		for _, c := range items {
			row.MemoryMB += c.MemoryMB
			row.DiskMB += c.DiskMB
			if c.GPU != "" && c.GPU != "none" {
				gpus[c.GPU] = true
			}
		}
		for g := range gpus {
			if row.GPU != "" {
				row.GPU += ", "
			}
			row.GPU += g
			if usage, _ := a.docker.GPUUsage(r.Context(), g); usage.MemoryTotalMB > 0 || usage.Percent > 0 {
				row.GPUPercent += usage.Percent
				row.GPUMemoryMB += usage.MemoryMB
				row.GPUMemoryTotalMB += usage.MemoryTotalMB
			}
		}
		if len(gpus) > 0 {
			row.GPUPercent = row.GPUPercent / float64(len(gpus))
		}
		if row.GPUMemoryTotalMB > 0 {
			row.GPUMemoryPct = row.GPUMemoryMB / row.GPUMemoryTotalMB * 100
		}
		out = append(out, row)
	}
	return out
}
