package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"mudp/internal/dockerx"
	"mudp/internal/httpx"
	"mudp/internal/store"
)

// networkGrants assembles the caller's grant picture: the networks granted to
// one of their groups, plus every network that carries a grant at all. The
// second set is what makes "bridge" behave sensibly — untouched it stays open to
// everyone, and the moment an admin picks groups for it, it belongs to those
// groups only.
//
// Errors are swallowed to the zero value: a grant lookup that fails must not
// break the Networks view, it just narrows it back to what the user owns.
func (a *App) networkGrants(u *store.User) dockerx.NetworkAccess {
	if u == nil {
		return dockerx.NetworkAccess{}
	}
	var access dockerx.NetworkAccess
	if names, err := a.db.NetworksForUser(u.ID); err == nil {
		access.Granted = make(map[string]bool, len(names))
		for _, n := range names {
			access.Granted[n] = true
		}
	}
	if byNetwork, err := a.db.NetworkGroupsByNetwork(); err == nil {
		access.Restricted = make(map[string]bool, len(byNetwork))
		for network, groups := range byNetwork {
			if len(groups) > 0 {
				access.Restricted[network] = true
			}
		}
	}
	return access
}

// attachableNetworks returns the full Docker names a user may attach a
// container to: their own mudp-managed networks, the networks granted to their
// groups, and "bridge" when it is open to them. It is the live list from Docker
// filtered by the same visibility rules the Networks view uses, and it is what
// CreateContainer enforces against — nothing outside it gets attached.
func (a *App) attachableNetworks(ctx context.Context, u *store.User) []string {
	if u == nil {
		return nil
	}
	nets, err := a.docker.ListNetworks(ctx, u.Username, u.Role == store.RoleAdmin, a.networkGrants(u))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(nets))
	for _, n := range nets {
		if n.Attachable {
			names = append(names, n.FullName)
		}
	}
	return names
}

// resolveAttachableNetworks maps the network names a client sent into full
// Docker names and verifies each one is attachable by the caller. Returns an
// error naming the first one that is not, so a request that tries to join a
// network nobody shared is refused outright rather than silently half-applied.
//
// A nil input stays nil: "networks" omitted means "leave them alone", which is
// not the same as "detach everything".
func (a *App) resolveAttachableNetworks(ctx context.Context, u *store.User, names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	allowed := make(map[string]bool)
	for _, n := range a.attachableNetworks(ctx, u) {
		allowed[n] = true
	}
	access := a.networkGrants(u)
	out := make([]string, 0, len(names))
	for _, raw := range names {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		full := resolveNetworkName(raw, u.Username, u.Role == store.RoleAdmin, access)
		if !allowed[full] {
			return nil, fmt.Errorf("network %q is not available to you", raw)
		}
		out = append(out, full)
	}
	return out, nil
}

// networks handles GET (list) and POST (create) for mudp-managed networks.
func (a *App) networks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.docker.ListNetworks(r.Context(), u.Username, u.Role == "admin", a.networkGrants(u))
		if err == nil && u.Role == store.RoleAdmin {
			// Admins manage the grants, so each row carries the groups it is
			// shared with. One query for all rows, not one per row.
			if byNetwork, gerr := a.db.NetworkGroupsByNetwork(); gerr == nil {
				for i := range items {
					items[i].Groups = byNetwork[items[i].FullName]
				}
			}
		}
		if err != nil {
			httpx.Logger(r).Error("list networks failed", "error", err)
			if dockerx.IsUnavailableError(err) {
				writeErr(w, http.StatusServiceUnavailable, "docker unavailable")
				return
			}
			respond(w, items, err)
			return
		}
		respond(w, items, nil)
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
	// RemoveNetwork is deliberately not granted-aware: a shared network is yours
	// to use, not to delete. Only its owner (or an admin) gets past this.
	if err := a.docker.RemoveNetwork(r.Context(), name, u.Username, u.Role == "admin"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Drop the grants with the network so a later network of the same name does
	// not inherit access from the deleted one.
	if err := a.db.DeleteNetworkGroups(name); err != nil {
		httpx.Logger(r).Error("clear network grants failed", "network", name, "error", err)
	}
	a.record(r, "network.delete", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resolveNetworkName normalizes a network identifier: if it lacks the mudp-
// prefix it's treated as a display name owned by username (matching the
// create/delete endpoints' backward-compat behavior). Docker's own built-in
// networks (bridge/host/none) and any network granted to the caller's groups
// are passed through unchanged — every user can see those rows in the Networks
// list and open their (read-only) details, so rewriting "bridge" into
// "mudp-<user>-net-bridge" would look up a network that never exists and turn a
// routine detail view into a server error.
//
// An admin's names are passed through wholesale: admins also see the host's
// pre-existing networks, whose names carry no mudp prefix and match no grant
// until one is shared. Namespacing those was what turned "details" on a plain
// `docker network create` network into "network mudp-<admin>-net-<name> not
// found".
func resolveNetworkName(raw, username string, admin bool, access dockerx.NetworkAccess) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if admin || strings.HasPrefix(raw, dockerx.Prefix) || dockerx.IsSystemNetworkName(raw) || access.Granted[raw] {
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
	access := a.networkGrants(u)
	admin := u.Role == store.RoleAdmin
	name := resolveNetworkName(r.URL.Query().Get("name"), u.Username, admin, access)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	detail, err := a.docker.InspectNetwork(r.Context(), name, u.Username, admin, access)
	respond(w, detail, err)
}

// networkAccess sets which user groups may see and use a network. Admin-only:
// it is the knob that shares a host network (or one user's network) with a
// group. It never changes ownership, so it cannot make a network deletable by
// anyone but its owner.
func (a *App) networkAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if u.Role != store.RoleAdmin {
		writeErr(w, http.StatusForbidden, "only an admin can share networks")
		return
	}
	var req struct {
		Name     string  `json:"name"`
		GroupIDs []int64 `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	name := strings.TrimSpace(req.Name)
	// The network must exist and be one an admin can see, so a typo (or a
	// crafted request) can't seed grants for a name that means nothing today and
	// something else tomorrow. host/none are excluded: joining them is either
	// impossible or hands over the host's interfaces, so there is nothing to
	// share. "bridge" is grantable — with no groups it stays open to everyone,
	// and with groups only they may attach to it.
	if dockerx.IsSystemNetworkName(name) && !dockerx.IsShareableSystemNetwork(name) {
		writeErr(w, http.StatusBadRequest, "Docker's host/none networks cannot be shared")
		return
	}
	if _, err := a.docker.InspectNetwork(r.Context(), name, u.Username, true, dockerx.NetworkAccess{}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.db.SetNetworkGroups(name, req.GroupIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "network.access", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	access := a.networkGrants(u)
	full := resolveNetworkName(req.Name, u.Username, u.Role == store.RoleAdmin, access)
	if full == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if err := a.docker.NetworkConnectContainer(r.Context(), full, req.ContainerID, u.Username, u.Role == "admin", access); err != nil {
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
	access := a.networkGrants(u)
	full := resolveNetworkName(req.Name, u.Username, u.Role == store.RoleAdmin, access)
	if full == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if err := a.docker.NetworkDisconnectContainer(r.Context(), full, req.ContainerID, u.Username, u.Role == "admin", access, req.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "network.disconnect", full)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
