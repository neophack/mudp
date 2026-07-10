package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

func (a *App) refreshRuntimeCache(ctx context.Context) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	sys := a.docker.SystemInfo(ctx)
	containers, err := a.docker.ListContainersWithSize(ctx, "", true)
	if err != nil {
		containers = nil
	}
	a.cacheMu.Lock()
	a.cachedSystem = sys
	if err == nil {
		a.cachedContainers = containers
	}
	a.cacheAt = time.Now()
	a.cacheMu.Unlock()
}

func (a *App) runtimeSystem() dockerx.SystemInfo {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	return a.cachedSystem
}

func (a *App) runtimeContainers(username string, admin bool) []dockerx.Container {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	out := make([]dockerx.Container, 0, len(a.cachedContainers))
	for _, c := range a.cachedContainers {
		if !admin {
			owner := c.Labels["mudp.user"]
			if owner != username && !strings.HasPrefix("/"+c.FullName, dockerx.UserContainerPrefix(username)) {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// collectResourceSnapshot gathers one resource sample for every container and
// persists it. It is safe to call concurrently; overlapping runs are skipped
// when the previous sample is recent enough.
func (a *App) collectResourceSnapshot(ctx context.Context) []store.ResourceSample {
	if time.Since(a.lastSnapshot) < 30*time.Second {
		return nil
	}
	users, err := a.db.Users()
	if err != nil {
		return nil
	}
	a.refreshRuntimeCache(ctx)
	containers := a.runtimeContainers("", true)
	usersByName := map[string]store.User{}
	for _, u := range users {
		usersByName[u.Username] = u
	}
	var samples []store.ResourceSample
	now := time.Now().Format(time.RFC3339)
	for _, c := range containers {
		u, ok := usersByName[c.Labels["mudp.user"]]
		if !ok {
			continue
		}
		s := store.ResourceSample{
			UserID: u.ID, Username: u.Username, ContainerID: c.ID, Container: c.Name,
			MemoryMB: c.MemoryMB, DiskMB: c.DiskMB, CreatedAt: now,
		}
		if c.State == "running" {
			if one, err := a.docker.SampleStats(ctx, c.ID); err == nil {
				s.CPUPercent = one.CPUPercent
				s.MemoryMB = one.MemoryMB
			}
		}
		s.GPUPercent = c.GPUPercent
		samples = append(samples, s)
	}
	if err := a.db.SaveResourceSamples(samples); err == nil {
		a.lastSnapshot = time.Now()
	}
	return samples
}

func (a *App) resourceHistory(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	since := time.Now().Add(-24 * time.Hour)
	items, err := a.db.ResourceSamples(u.ID, u.Role == "admin", since)
	respond(w, items, err)
}

// StartBackgroundJobs launches periodic maintenance goroutines: resource
// sampling, audit/resource pruning, and WAL checkpointing. The returned
// function stops the jobs; call it during graceful shutdown.
func (a *App) StartBackgroundJobs(ctx context.Context) func() {
	cache := time.NewTicker(15 * time.Second)
	sample := time.NewTicker(60 * time.Second)
	prune := time.NewTicker(24 * time.Hour)
	checkpoint := time.NewTicker(60 * time.Minute)

	// Run an initial sample and prune so a fresh server has data immediately
	// and does not start with stale records from a previous process.
	a.collectResourceSnapshot(ctx)
	a.pruneOldData(ctx)
	if n, reclaimed, err := a.docker.PruneImages(ctx); err == nil && n > 0 {
		log.Printf("pruned %d dangling images (%d bytes reclaimed)", n, reclaimed)
	}

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-cache.C:
				a.refreshRuntimeCache(ctx)
			case <-sample.C:
				a.collectResourceSnapshot(ctx)
			case <-prune.C:
				a.pruneOldData(ctx)
			case <-checkpoint.C:
				if err := a.db.Checkpoint(); err != nil {
					// Best-effort; noisy logs on shutdown are unhelpful.
				}
			case <-stop:
				cache.Stop()
				sample.Stop()
				prune.Stop()
				checkpoint.Stop()
				return
			}
		}
	}()
	return func() { close(stop) }
}

func (a *App) pruneOldData(ctx context.Context) {
	if err := a.db.PruneAuditLogs(time.Now().Add(-90 * 24 * time.Hour)); err != nil {
		// Best-effort; do not fail requests due to pruning errors.
	}
	if err := a.db.PruneResourceSamples(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		// Best-effort.
	}
}

func (a *App) adminProcesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.docker.TopProcesses(r.Context(), a.runtimeContainers("", true)))
}
