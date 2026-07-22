package server

import (
	"context"
	"net"
	"net/http"
	"strings"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// wrtPolicyResponse is what GET /api/wrt returns. It bundles the durable
// policy with a real-time read of the gateway container, so the UI can show
// whether the router is actually running alongside the configured image /
// addressing.
type wrtPolicyResponse struct {
	store.WRTPolicy
	// GatewayRunning reports whether the ImmortalWrt gateway container is up.
	GatewayRunning bool `json:"gatewayRunning"`
	// GatewayImage is the image the running container actually uses (may differ
	// from policy.Image right after a save that triggers a rebuild).
	GatewayImage string `json:"gatewayImage"`
	// GatewayContainer is the running container's name, or "" when not running.
	GatewayContainer string `json:"gatewayContainer,omitempty"`
	// MeshContainers is how many user containers are currently on the LAN
	// network — gives the admin a sense of the isolation blast radius.
	MeshContainers int `json:"meshContainers"`
}

// wrtSettings is the admin GET/POST handler for the WRT gateway policy.
// Mirrors mcpSettings: admin gate, decode into the store struct, validate every
// subnet/gateway, persist, hot-reload the networks + gateway, audit, return ok.
func (a *App) wrtSettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		pol, err := a.db.WRTPolicy()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := wrtPolicyResponse{WRTPolicy: pol}
		st := a.docker.WRTStatus(r.Context())
		resp.GatewayRunning = st.Running
		resp.GatewayImage = st.Image
		resp.GatewayContainer = st.Container
		resp.MeshContainers = a.meshContainerCount(r.Context())
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		var req store.WRTPolicy
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Image = strings.TrimSpace(req.Image)
		if req.Image == "" {
			req.Image = store.DefaultWRTPolicy().Image
		}
		req.LANSubnet = strings.TrimSpace(req.LANSubnet)
		req.LANGateway = strings.TrimSpace(req.LANGateway)
		req.WANSubnet = strings.TrimSpace(req.WANSubnet)
		req.WANGateway = strings.TrimSpace(req.WANGateway)
		req.WANIP = strings.TrimSpace(req.WANIP)

		// Validate: subnets must be real CIDRs; gateways/IPs must be real IPs
		// (and the gateway IPs should sit inside their subnet, but we don't
		// enforce that strictly — the admin may be mid-reconfiguration).
		for field, val := range map[string]string{
			"lanSubnet": req.LANSubnet, "wanSubnet": req.WANSubnet,
		} {
			if _, _, err := net.ParseCIDR(val); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid "+field+" CIDR: "+val)
				return
			}
		}
		for field, val := range map[string]string{
			"lanGateway": req.LANGateway, "wanGateway": req.WANGateway, "wanIp": req.WANIP,
		} {
			if net.ParseIP(val) == nil {
				writeErr(w, http.StatusBadRequest, "invalid "+field+" IP: "+val)
				return
			}
		}

		if err := a.db.SaveWRTPolicy(req); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Hot-reload: recreate/reconcile the networks + gateway to match. This
		// is best-effort — subnet changes can't apply to an existing network
		// (Docker IPAM is immutable), so the admin may need to remove the old
		// mesh/WAN networks and reboot. Failures are logged, not returned, so
		// the save still succeeds and the policy is durable for next boot.
		a.applyWRTPolicy(r.Context(), req)
		a.record(r, "wrt.policy.update", "wrt")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// meshContainerCount returns how many containers are currently attached to the
// mudp-mesh (gateway LAN) network. Best-effort: returns 0 on any error so the
// settings panel degrades gracefully rather than failing the whole GET.
func (a *App) meshContainerCount(ctx context.Context) int {
	nets, err := a.docker.RawNetworkList(ctx)
	if err != nil {
		return 0
	}
	for _, n := range nets {
		if n.Name == dockerx.MeshNetworkName {
			return len(n.Containers)
		}
	}
	return 0
}
