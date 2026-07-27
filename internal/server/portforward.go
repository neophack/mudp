package server

// mudp-managed port forwarding. See internal/portfwd for why it exists (a host
// whose firewall is owned by OpenWrt cannot publish a Docker port, but the
// container's own LAN address works) and internal/dockerx/forward.go for how a
// container records the mapping it wants.
//
// This file is the join between the two: it decides which networks forward,
// reconciles the running listeners against container state, and serves the
// admin settings.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"mudp/internal/dockerx"
	"mudp/internal/httpx"
	"mudp/internal/portfwd"
	"mudp/internal/store"
)

// forwardNetworks returns the networks an administrator marked for mudp
// forwarding. A lookup failure reads as "none": forwarding is the exception, so
// falling back to Docker publishing is the safer direction to fail in.
func (a *App) forwardNetworks() []string {
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		return nil
	}
	return cfg.Networks
}

// SyncPortForward reconciles the running listeners against what the containers
// currently ask for. It is idempotent and cheap (one container list plus a diff),
// and it is called from refreshRuntimeCache — so it runs at boot, on the 15s
// sweep, and after every create/start/stop, without any of those having to know
// forwarding exists.
//
// A container that restarts onto a new address is repointed by this; a container
// that is removed has its host port released by this.
func (a *App) SyncPortForward(ctx context.Context) error {
	var errs []error
	rules, err := a.docker.ForwardRules(ctx, a.forwardNetworks())
	if err != nil {
		// Docker is unreachable. Reconciling with an empty container list would
		// read as "every container is gone" and tear down forwards whose
		// containers are still running perfectly well, so the ones already
		// serving are carried over untouched until the daemon answers again.
		errs = append(errs, err)
		rules = rulesFromSource(a.forward.Rules(), "container")
	}
	// Manual rules are resolved even when the container lookup failed: one that
	// names a fixed address does not depend on Docker at all, and it is exactly
	// the rule an admin adds when the container-driven ones fall short.
	manual, merr := a.manualForwardRules(ctx)
	if merr != nil {
		errs = append(errs, merr)
	}
	if err := a.forward.Apply(append(rules, manual...)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// rulesFromSource filters rules by where they came from ("container"/"manual").
func rulesFromSource(rules []portfwd.Rule, source string) []portfwd.Rule {
	out := make([]portfwd.Rule, 0, len(rules))
	for _, r := range rules {
		if r.Source == source {
			out = append(out, r)
		}
	}
	return out
}

// manualForwardRules turns the stored hand-added forwards into rules, resolving
// the ones that name a container to the address it holds right now. That
// resolution happens on every sweep, which is what lets a manual forward follow
// a container across restarts instead of pointing at an address it has lost.
func (a *App) manualForwardRules(ctx context.Context) ([]portfwd.Rule, error) {
	stored, err := a.db.ManualForwards()
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return nil, nil
	}
	// A failure to list containers only costs the rules that name one: the rest
	// are built anyway, and the error travels back with them so the page can say
	// why a container-following rule is not listening.
	var targets []dockerx.ContainerTarget
	var terr error
	if needsContainers(stored) {
		targets, terr = a.docker.ContainerTargets(ctx)
	}
	out := make([]portfwd.Rule, 0, len(stored))
	for _, f := range stored {
		r := portfwd.Rule{
			HostPort:   f.HostPort,
			Proto:      f.Proto,
			TargetIP:   f.TargetIP,
			TargetPort: f.TargetPort,
			Owner:      f.Owner,
			Name:       f.Note,
			Source:     "manual",
			ManualID:   f.ID,
			Note:       f.Note,
		}
		if f.ContainerID != "" {
			t, ok := findTarget(targets, f.ContainerID)
			if ok {
				r.TargetIP = t.IP
				r.Container = t.ID
				if r.Owner == "" {
					r.Owner = t.Owner
				}
				if r.Name == "" {
					r.Name = t.Name
				}
			}
		}
		if r.Name == "" {
			r.Name = fmt.Sprintf("manual %d/%s", f.HostPort, f.Proto)
		}
		out = append(out, r)
	}
	return out, terr
}

// needsContainers reports whether any stored forward names a container, so the
// container list is only fetched when a rule actually depends on it.
func needsContainers(fs []store.ManualForward) bool {
	for _, f := range fs {
		if f.ContainerID != "" {
			return true
		}
	}
	return false
}

// findTarget matches a stored container reference against the live containers.
// Ids are matched by prefix so a shortened id (what the console shows) resolves.
func findTarget(targets []dockerx.ContainerTarget, ref string) (dockerx.ContainerTarget, bool) {
	for _, t := range targets {
		if t.ID == ref || t.FullName == ref || t.Name == ref {
			return t, true
		}
	}
	for _, t := range targets {
		if len(ref) >= 8 && strings.HasPrefix(t.ID, ref) {
			return t, true
		}
	}
	return dockerx.ContainerTarget{}, false
}

// syncPortForwardLogged runs a reconcile and logs what it could not do. Used by
// the callers that have nowhere to return an error to.
func (a *App) syncPortForwardLogged(ctx context.Context) {
	if err := a.SyncPortForward(ctx); err != nil {
		log.Printf("port forward: %v", err)
	}
}

// --- management API ---

// portForwardSettings reads and writes which networks mudp forwards for, and
// reports the listeners currently running. Admin-only: it changes how every
// container on the named networks is published.
//
// GET|POST /api/admin/network/forward
func (a *App) portForwardSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.db.PortForwardConfig()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"networks": cfg.Networks,
			"rules":    a.forward.Status(),
		})
	case http.MethodPost:
		var req struct {
			Networks []string `json:"networks"`
			// NetworksRaw is the textarea form: one name per line or comma
			// separated. Either field may be used; both are normalised the same.
			NetworksRaw string `json:"networksRaw"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		names := req.Networks
		if strings.TrimSpace(req.NetworksRaw) != "" {
			names = append(names, store.ParsePortForwardNetworks(req.NetworksRaw)...)
		}
		cfg := store.NormalizePortForwardConfig(store.PortForwardConfig{Networks: names})
		if err := a.db.SavePortForwardConfig(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Apply immediately so the admin sees the effect on this response rather
		// than up to 15 seconds later. A rule that cannot bind is reported as a
		// warning, not an error: the setting is saved either way, and the next
		// sweep retries it (the port may free up, or the container may restart).
		warning := ""
		if err := a.SyncPortForward(r.Context()); err != nil {
			warning = err.Error()
			httpx.Logger(r).Error("port forward reconcile reported problems", "error", err)
		}
		target := "off"
		if cfg.Enabled() {
			target = strings.Join(cfg.Networks, ",")
		}
		a.record(r, "network.forward.settings", target)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"networks": cfg.Networks,
			"rules":    a.forward.Status(),
			"warning":  warning,
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// forwardsPage serves everything the admin Port forwarding page shows in one
// request: the running listeners (both container-derived and manual, with the
// owner and container behind each), the stored manual rules, the networks that
// forward, and the containers a new manual rule can point at.
//
// GET /api/admin/forwards
func (a *App) forwardsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	manual, err := a.db.ManualForwards()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Targets double as the "which container?" list in the add dialog and as the
	// name/owner lookup for rules whose container has since been renamed.
	targets, terr := a.docker.ContainerTargets(r.Context())
	if terr != nil {
		httpx.Logger(r).Error("list forward targets failed", "error", terr)
	}
	// The page is where an admin looks when a forward is not working, so tell
	// them what the last reconcile could not do rather than leaving the row
	// silently absent.
	warning := ""
	if err := a.SyncPortForward(r.Context()); err != nil {
		warning = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"networks": cfg.Networks,
		"rules":    a.forward.Status(),
		"manual":   manual,
		"targets":  targets,
		"warning":  warning,
	})
}

// forwardAdd stores a manual forward and brings it up. Admin-only: it binds a
// host port and can point at any address the host can reach.
//
// POST /api/admin/forwards
func (a *App) forwardAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req struct {
		HostPort    int    `json:"hostPort"`
		Proto       string `json:"proto"`
		ContainerID string `json:"containerId"`
		TargetIP    string `json:"targetIp"`
		TargetPort  int    `json:"targetPort"`
		Owner       string `json:"owner"`
		Note        string `json:"note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f := store.ManualForward{
		HostPort: req.HostPort, Proto: req.Proto,
		ContainerID: strings.TrimSpace(req.ContainerID),
		TargetIP:    strings.TrimSpace(req.TargetIP),
		TargetPort:  req.TargetPort,
		Owner:       strings.TrimSpace(req.Owner),
		Note:        strings.TrimSpace(req.Note),
		CreatedBy:   u.Username,
	}
	if err := a.validateManualForward(r.Context(), &f); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := a.db.AddManualForward(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Bring it up now. If the port turns out to be unusable the rule is kept and
	// reported rather than discarded: the admin typed it deliberately, and the
	// next sweep retries once whatever holds the port lets go.
	warning := ""
	if err := a.SyncPortForward(r.Context()); err != nil {
		warning = err.Error()
	}
	a.record(r, "forward.add", fmt.Sprintf("%d/%s → %s", saved.HostPort, saved.Proto, manualTargetLabel(saved)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "forward": saved, "warning": warning})
}

// forwardDelete removes a manual forward and stops its listener.
//
// POST /api/admin/forwards/delete
func (a *App) forwardDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := a.db.DeleteManualForward(req.ID); err != nil {
		writeErr(w, http.StatusBadRequest, "forward not found")
		return
	}
	if err := a.SyncPortForward(r.Context()); err != nil {
		httpx.Logger(r).Error("port forward reconcile reported problems", "error", err)
	}
	a.record(r, "forward.delete", strconv.FormatInt(req.ID, 10))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validateManualForward rejects a rule that could not work or should not be
// allowed, before it is stored: a port the console itself serves on would take
// the UI down on the next reconcile, and a target nobody can name is just a
// listener that refuses every connection.
func (a *App) validateManualForward(ctx context.Context, f *store.ManualForward) error {
	f.Proto = strings.ToLower(strings.TrimSpace(f.Proto))
	if f.Proto == "" {
		f.Proto = "tcp"
	}
	if f.Proto != "tcp" && f.Proto != "udp" {
		return fmt.Errorf("protocol must be tcp or udp")
	}
	if f.HostPort < 1 || f.HostPort > 65535 {
		return fmt.Errorf("host port must be between 1 and 65535")
	}
	if f.TargetPort < 1 || f.TargetPort > 65535 {
		return fmt.Errorf("target port must be between 1 and 65535")
	}
	if p := listenPort(a.cfg.Addr); p != 0 && p == f.HostPort && f.Proto == "tcp" {
		return fmt.Errorf("host port %d is the console's own port", f.HostPort)
	}
	if f.ContainerID == "" && f.TargetIP == "" {
		return fmt.Errorf("choose a container or type a target address")
	}
	if f.ContainerID != "" {
		targets, err := a.docker.ContainerTargets(ctx)
		if err != nil {
			return fmt.Errorf("cannot read containers: %w", err)
		}
		t, ok := findTarget(targets, f.ContainerID)
		if !ok {
			return fmt.Errorf("container %q not found", f.ContainerID)
		}
		// Store the full id: a name can be reused by a later container, and the
		// rule must not silently start relaying to that one.
		f.ContainerID = t.ID
		if f.Owner == "" {
			f.Owner = t.Owner
		}
		f.TargetIP = ""
	} else if net.ParseIP(f.TargetIP) == nil {
		return fmt.Errorf("%q is not an IP address", f.TargetIP)
	}
	// A host port already relayed by a container's own rule cannot be taken by a
	// manual one: the container would lose its port on the next sweep, and which
	// of the two wins would depend on ordering.
	rules, err := a.docker.ForwardRules(ctx, a.forwardNetworks())
	if err == nil {
		for _, r := range rules {
			if r.HostPort == f.HostPort && r.Proto == f.Proto {
				return fmt.Errorf("host port %d/%s is already forwarded for container %q", f.HostPort, f.Proto, r.Name)
			}
		}
	}
	return nil
}

// manualTargetLabel renders a stored forward's target for the audit trail.
func manualTargetLabel(f store.ManualForward) string {
	if f.ContainerID != "" {
		id := f.ContainerID
		if len(id) > 12 {
			id = id[:12]
		}
		return fmt.Sprintf("container %s:%d", id, f.TargetPort)
	}
	return net.JoinHostPort(f.TargetIP, strconv.Itoa(f.TargetPort))
}

// networkForward turns forwarding on or off for one network. It is the same
// setting portForwardSettings writes, reached from the row on the Networks
// page — which is where an admin is when they discover that this network's
// published ports do not work.
//
// POST /api/networks/forward {"name": "...", "forward": true}
func (a *App) networkForward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if u.Role != store.RoleAdmin {
		writeErr(w, http.StatusForbidden, "only an admin can change port forwarding")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Forward bool   `json:"forward"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	name := strings.TrimSpace(req.Name)
	// Forwarding replaces what Docker does for containers on this network, so
	// the network has to exist and be one a container can actually join. A typo
	// would otherwise sit in the settings meaning nothing.
	if dockerx.IsSystemNetworkName(name) && !dockerx.IsShareableSystemNetwork(name) {
		writeErr(w, http.StatusBadRequest, "Docker's host/none networks carry no container ports")
		return
	}
	if req.Forward {
		if _, err := a.docker.InspectNetwork(r.Context(), name, u.Username, true, dockerx.NetworkAccess{}); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Drop every name that refers to this network before adding it back, so
	// turning a network off also clears an older entry that named it by its
	// display name rather than its full Docker name.
	kept := make([]string, 0, len(cfg.Networks)+1)
	for _, n := range cfg.Networks {
		if !dockerx.NetworkNameMatches(name, n) {
			kept = append(kept, n)
		}
	}
	if req.Forward {
		kept = append(kept, name)
	}
	cfg = store.NormalizePortForwardConfig(store.PortForwardConfig{Networks: kept})
	if err := a.db.SavePortForwardConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	warning := ""
	if err := a.SyncPortForward(r.Context()); err != nil {
		warning = err.Error()
		httpx.Logger(r).Error("port forward reconcile reported problems", "error", err)
	}
	verb := "off"
	if req.Forward {
		verb = "on"
	}
	a.record(r, "network.forward", name+" "+verb)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"forward":  req.Forward,
		"networks": cfg.Networks,
		"warning":  warning,
	})
}

// portForwardNote describes the forwarding setting to a non-admin, so the
// create wizard can say why a port on this network behaves differently. It
// carries no host detail beyond the network names the user can already see.
//
// GET /api/network/forward
func (a *App) portForwardInfo(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  cfg.Enabled(),
		"networks": cfg.Networks,
	})
}
