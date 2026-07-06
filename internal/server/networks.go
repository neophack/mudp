package server

import (
	"net/http"
	"strings"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// networks handles GET (list) and POST (create) for mudp-managed networks.
func (a *App) networks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.docker.ListNetworks(r.Context(), u.Username, u.Role == "admin")
		respond(w, items, err)
	case http.MethodPost:
		if u.Role != store.RoleAdmin {
			writeErr(w, http.StatusForbidden, "only an admin can create networks")
			return
		}
		var req struct {
			Name   string            `json:"name"`
			Driver string            `json:"driver"`
			Subnet string            `json:"subnet"`
			Labels map[string]string `json:"labels"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, "name is required")
			return
		}
		id, err := a.docker.CreateNetwork(r.Context(), dockerx.CreateNetworkOptions{
			Username: u.Username,
			Name:     req.Name,
			Driver:   req.Driver,
			Subnet:   req.Subnet,
			Labels:   req.Labels,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.record(r, "network.create", req.Name)
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// networkDelete removes a single network (ownership-guarded server-side).
func (a *App) networkDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot delete networks")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	name := req.Name
	if !strings.HasPrefix(name, dockerx.Prefix) {
		// Backward compatibility: the UI used to send the display name.
		name = dockerx.NetworkFullName(u.Username, name)
	}
	if err := a.docker.RemoveNetwork(r.Context(), name, u.Username, u.Role == "admin"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "network.delete", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
