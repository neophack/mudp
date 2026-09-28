package server

import (
	"net/http"
	"time"

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
// trip: environment info, the caller's container rollup, (admin only) the
// per-user usage summary, and the current user's profile (for the Feishu card).
type dashboardResponse struct {
	System  dockerx.SystemInfo `json:"system"`
	Mine    mineRollup         `json:"mine"`
	Usage   []usageRow         `json:"usage,omitempty"`
	IsAdmin bool               `json:"isAdmin"`
	User    store.User         `json:"user"`
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
	// Admins see platform-wide counts; everyone else sees only their own
	// resource footprint so the dashboard reflects what they actually own.
	sys := a.dashboardSystem(u.Username, u.Role == "admin")

	items := a.runtimeContainers(u.Username, u.Role == "admin")
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

	resp := dashboardResponse{System: sys, Mine: mine, IsAdmin: u.Role == "admin", User: *u}
	if u.Role == "admin" {
		resp.Usage = buildUsageFromContainers(r, a, items)
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildUsage computes the admin per-user usage table. Extracted so the
// dashboard and the standalone usage endpoint share one implementation.
func buildUsage(r *http.Request, a *App) []usageRow {
	items := a.runtimeContainers("", true)
	return buildUsageFromContainers(r, a, items)
}

func (a *App) dashboardSystem(username string, admin bool) dockerx.SystemInfo {
	if admin {
		return a.runtimeSystem()
	}
	// The caller's own view, derived in memory from the shared 15s sweep: host
	// fields come from the cached platform snapshot, per-user resource counts
	// from the cached container list plus the sweep's scope basis. The route
	// polls every few seconds per user, so re-querying Docker here (the old
	// SystemInfoForUser path) multiplied into hundreds of daemon calls per
	// minute across the user base.
	return a.scopedSystem(username)
}

// scopedSystem assembles the per-user dashboard snapshot without any Docker
// calls: host facts are copied from the cached platform SystemInfo, container
// stats come from the caller's own cached containers, and images/volumes/
// networks are narrowed from the sweep's ScopeBasis by owner label and image
// reference.
func (a *App) scopedSystem(username string) dockerx.SystemInfo {
	sys := a.runtimeSystem()
	a.cacheMu.RLock()
	basis := a.cachedBasis
	a.cacheMu.RUnlock()
	sys.ServerTime = time.Now().Unix()

	cs := dockerx.ContainerStats{}
	imageIDs := map[string]bool{}
	for _, c := range a.runtimeContainers(username, false) {
		cs.Total++
		switch c.State {
		case "running":
			cs.Running++
		case "paused":
			cs.Paused++
		case "exited", "dead", "created":
			cs.Stopped++
		}
		switch c.Health {
		case "healthy":
			cs.Healthy++
		case "unhealthy":
			cs.Unhealthy++
		}
		if c.ImageID != "" {
			imageIDs[c.ImageID] = true
		}
	}
	sys.Containers = cs

	// Images: the distinct non-derived images the user's containers reference
	// (images carry no owner label, so reference is the ownership signal).
	var imgCount int
	var imgSize int64
	for id := range imageIDs {
		if size, ok := basis.ImageSizes[id]; ok {
			imgCount++
			imgSize += size
		}
	}
	sys.Images = dockerx.ResourceStats{Count: imgCount, SizeB: imgSize, SizeMB: dockerx.SizeMB(imgSize)}

	// Volumes and networks: the user's own, plus Docker's built-in networks so
	// the tile stays consistent with the Networks page.
	var volCount int
	var volSize int64
	for _, v := range basis.Volumes {
		if v.Owner == username {
			volCount++
			volSize += v.SizeB
		}
	}
	sys.Volumes = dockerx.ResourceStats{Count: volCount, SizeB: volSize, SizeMB: dockerx.SizeMB(volSize)}

	nets := 0
	for _, n := range basis.Networks {
		if n.System || (n.Managed && n.Owner == username) {
			nets++
		}
	}
	sys.Networks = nets
	return sys
}

func buildUsageFromContainers(r *http.Request, a *App, items []dockerx.Container) []usageRow {
	users, err := a.db.Users()
	if err != nil {
		return nil
	}
	byUser := map[string][]dockerx.Container{}
	for _, c := range items {
		owner := c.Labels["mudp.user"]
		if owner == "" {
			continue
		}
		byUser[owner] = append(byUser[owner], c)
	}
	out := make([]usageRow, 0, len(users))
	for _, u := range users {
		userItems := byUser[u.Username]
		row := usageRow{User: u, Containers: len(userItems)}
		gpus := map[string]bool{}
		for _, c := range userItems {
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
