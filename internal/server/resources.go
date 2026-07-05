package server

import (
	"context"
	"net/http"
	"time"

	"mudp/internal/store"
)

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
	var samples []store.ResourceSample
	now := time.Now().Format(time.RFC3339)
	for _, u := range users {
		containers, _ := a.docker.ListContainers(ctx, u.Username, false)
		for _, c := range containers {
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
	sample := time.NewTicker(60 * time.Second)
	prune := time.NewTicker(24 * time.Hour)
	checkpoint := time.NewTicker(60 * time.Minute)

	// Run an initial sample and prune so a fresh server has data immediately
	// and does not start with stale records from a previous process.
	a.collectResourceSnapshot(ctx)
	a.pruneOldData(ctx)

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-sample.C:
				a.collectResourceSnapshot(ctx)
			case <-prune.C:
				a.pruneOldData(ctx)
			case <-checkpoint.C:
				if err := a.db.Checkpoint(); err != nil {
					// Best-effort; noisy logs on shutdown are unhelpful.
				}
			case <-stop:
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
	items, err := a.docker.ListContainers(r.Context(), "", true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.docker.TopProcesses(r.Context(), items))
}
