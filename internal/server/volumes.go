package server

import (
	"net/http"
	"strings"

	"mudp/internal/dockerx"
)

// volumes handles GET (list) and POST (create) for mudp-managed volumes.
func (a *App) volumes(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.docker.ListVolumes(r.Context(), u.Username, u.Role == "admin")
		respond(w, items, err)
	case http.MethodPost:
		if !canMutate(u) {
			writeErr(w, http.StatusForbidden, "read-only role cannot create volumes")
			return
		}
		var req struct {
			Name       string            `json:"name"`
			Driver     string            `json:"driver"`
			DriverOpts map[string]string `json:"driverOpts"`
			Labels     map[string]string `json:"labels"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, "name is required")
			return
		}
		full, err := a.docker.CreateVolume(r.Context(), dockerx.CreateVolumeOptions{
			Username:   u.Username,
			Name:       req.Name,
			Driver:     req.Driver,
			DriverOpts: req.DriverOpts,
			Labels:     req.Labels,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.record(r, "volume.create", full)
		writeJSON(w, http.StatusOK, map[string]string{"name": full})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// volumeDelete removes a single volume (ownership-guarded server-side).
func (a *App) volumeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot delete volumes")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Force bool   `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := a.docker.RemoveVolume(r.Context(), req.Name, u.Username, u.Role == "admin", req.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "volume.delete", req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// volumePrune removes unused dangling volumes owned by the caller (admin: all).
func (a *App) volumePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot prune volumes")
		return
	}
	count, bytes, err := a.docker.PruneVolumes(r.Context(), u.Username, u.Role == "admin")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "volume.prune", u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"removed": count, "bytesFreed": bytes})
}
