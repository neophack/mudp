package server

import (
	"net/http"
	"time"

	"mudp/internal/store"
)

func (a *App) collectResourceSnapshot(r *http.Request) []store.ResourceSample {
	users, err := a.db.Users()
	if err != nil {
		return nil
	}
	var samples []store.ResourceSample
	now := time.Now().Format(time.RFC3339)
	for _, u := range users {
		containers, _ := a.docker.ListContainers(r.Context(), u.Username, false)
		for _, c := range containers {
			s := store.ResourceSample{
				UserID: u.ID, Username: u.Username, ContainerID: c.ID, Container: c.Name,
				MemoryMB: c.MemoryMB, DiskMB: c.DiskMB, CreatedAt: now,
			}
			if c.State == "running" {
				if one, err := a.docker.SampleStats(r.Context(), c.ID); err == nil {
					s.CPUPercent = one.CPUPercent
					s.MemoryMB = one.MemoryMB
				}
			}
			s.GPUPercent = c.GPUPercent
			samples = append(samples, s)
		}
	}
	_ = a.db.SaveResourceSamples(samples)
	return samples
}

func (a *App) resourceHistory(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	// Opportunistically store a fresh point so a newly installed server starts
	// drawing a history immediately. A scheduler can call the same endpoint.
	a.collectResourceSnapshot(r)
	since := time.Now().Add(-24 * time.Hour)
	items, err := a.db.ResourceSamples(u.ID, u.Role == "admin", since)
	respond(w, items, err)
}

func (a *App) adminProcesses(w http.ResponseWriter, r *http.Request) {
	items, err := a.docker.ListContainers(r.Context(), "", true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.docker.TopProcesses(r.Context(), items))
}
