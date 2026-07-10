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
			Name         string            `json:"name"`
			Driver       string            `json:"driver"`
			Subnet       string            `json:"subnet"`
			Gateway      string            `json:"gateway"`
			IPRange      string            `json:"ipRange"`
			IPv6         bool              `json:"ipv6"`
			AuxAddresses map[string]string `json:"auxAddresses"`
			Labels       map[string]string `json:"labels"`
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
			Username:     u.Username,
			Name:         req.Name,
			Driver:       req.Driver,
			Subnet:       req.Subnet,
			Gateway:      req.Gateway,
			IPRange:      req.IPRange,
			IPv6:         req.IPv6,
			AuxAddresses: req.AuxAddresses,
			Labels:       req.Labels,
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

// resolveNetworkName normalizes a network identifier: if it lacks the mudp-
// prefix it's treated as a display name owned by username (matching the
// create/delete endpoints' backward-compat behavior).
func resolveNetworkName(raw, username string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, dockerx.Prefix) {
		return raw
	}
	return dockerx.NetworkFullName(username, raw)
}

// networkInspect returns a network's summary plus its attached containers for
// the network detail modal. Read-only (any activated user), ownership-guarded.
func (a *App) networkInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	name := resolveNetworkName(r.URL.Query().Get("name"), u.Username)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	detail, err := a.docker.InspectNetwork(r.Context(), name, u.Username, u.Role == "admin")
	respond(w, detail, err)
}

// networkConnect attaches a container to a network. Mutating; ownership is
// checked on both the network and the container (the caller must own both).
func (a *App) networkConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify networks")
		return
	}
	var req struct {
		Name        string `json:"name"`
		ContainerID string `json:"containerId"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ContainerID) == "" {
		writeErr(w, http.StatusBadRequest, "name and containerId are required")
		return
	}
	full := resolveNetworkName(req.Name, u.Username)
	if full == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if err := a.docker.NetworkConnectContainer(r.Context(), full, req.ContainerID, u.Username, u.Role == "admin"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "network.connect", full)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// networkDisconnect detaches a container from a network. Mutating.
func (a *App) networkDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify networks")
		return
	}
	var req struct {
		Name        string `json:"name"`
		ContainerID string `json:"containerId"`
		Force       bool   `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ContainerID) == "" {
		writeErr(w, http.StatusBadRequest, "name and containerId are required")
		return
	}
	full := resolveNetworkName(req.Name, u.Username)
	if full == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if err := a.docker.NetworkDisconnectContainer(r.Context(), full, req.ContainerID, u.Username, u.Role == "admin", req.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "network.disconnect", full)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
