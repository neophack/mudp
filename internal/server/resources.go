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
	containers, err := a.docker.ListContainersWithSize(ctx, "", true, a.forwardNetworks())
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

	// Port forwarding is reconciled from the same sweep as the container cache,
	// because it is driven by exactly the same facts: which containers exist and
	// what address each holds. That gives it a boot pass, a 15s heartbeat, and a
	// pass after every create/start/stop for free — a container that restarts
	// onto a new IP is repointed without anyone asking.
	a.syncPortForwardLogged(ctx)
}

// triggerRuntimeCacheRefresh runs a cache refresh in the background so that
// mutating endpoints (create, start, stop, etc.) do not return stale data on
// the next list request. It uses a detached context so the refresh survives
// the HTTP request lifecycle.
func (a *App) triggerRuntimeCacheRefresh() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.refreshRuntimeCache(ctx)
	}()
}

// evictCachedContainers removes the given container IDs from the in-memory
// cache immediately after a successful mutating operation. This prevents the
// list endpoint from returning stale data while the asynchronous full refresh
// is still in progress. The IDs may be short prefixes; any cached container
// whose full ID starts with one of the supplied prefixes is removed.
func (a *App) evictCachedContainers(ids ...string) {
	if len(ids) == 0 {
		return
	}
	prefixes := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			prefixes = append(prefixes, id)
		}
	}
	if len(prefixes) == 0 {
		return
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	filtered := make([]dockerx.Container, 0, len(a.cachedContainers))
	for _, c := range a.cachedContainers {
		keep := true
		for _, p := range prefixes {
			if c.ID == p || strings.HasPrefix(c.ID, p) {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, c)
		}
	}
	a.cachedContainers = filtered
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
	a.snapshotMu.Lock()
	if time.Since(a.lastSnapshot) < 30*time.Second {
		a.snapshotMu.Unlock()
		return nil
	}
	a.snapshotMu.Unlock()
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
		a.snapshotMu.Lock()
		a.lastSnapshot = time.Now()
		a.snapshotMu.Unlock()
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
	// backupTick fires every minute so we can hit an exact HH:MM schedule.
	backupTick := time.NewTicker(60 * time.Second)
	// processWatchTick polls watched container processes for exits (see
	// processes.go).
	processWatchTick := time.NewTicker(processWatchInterval)

	stop := make(chan struct{})
	go func() {
		// Initial passes (process watch, resource sample, prune) used to run
		// synchronously before the HTTP listener bound. Every one of them
		// touches Docker, so a hung daemon (desktop wedge, API proxy stall)
		// kept the console from ever starting. Run them here instead, under a
		// bounded context, so the listener binds immediately and a stuck
		// daemon only delays the first data point instead of the whole server.
		initCtx, cancelInit := context.WithTimeout(ctx, 60*time.Second)
		a.watchProcesses(initCtx)
		a.collectResourceSnapshot(initCtx)
		a.pruneOldData(initCtx)
		if n, reclaimed, err := a.docker.PruneImages(initCtx); err == nil && n > 0 {
			log.Printf("pruned %d dangling images (%d bytes reclaimed)", n, reclaimed)
		}
		cancelInit()
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
			case <-backupTick.C:
				a.maybeRunScheduledBackup(ctx)
			case <-processWatchTick.C:
				a.watchProcesses(ctx)
			case <-stop:
				cache.Stop()
				sample.Stop()
				prune.Stop()
				checkpoint.Stop()
				backupTick.Stop()
				processWatchTick.Stop()
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
	// Access-log retention is admin-configurable; fall back to 90 days when unset.
	retention := 90
	if s := a.securitySettings(); s.RetentionDays > 0 {
		retention = s.RetentionDays
	}
	if err := a.db.PruneAccessLogs(time.Now().Add(-time.Duration(retention) * 24 * time.Hour)); err != nil {
		// Best-effort.
	}
	// MCP usage & attack logs are kept for one month: long enough to review what
	// an agent did and who probed the external port, short enough to bound growth.
	if err := a.db.PruneMCPUsageLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		// Best-effort.
	}
	if err := a.db.PruneMCPAttackLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		// Best-effort.
	}
}

func (a *App) adminProcesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.docker.TopProcesses(r.Context(), a.runtimeContainers("", true)))
}
