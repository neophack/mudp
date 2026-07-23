package server

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// wrtDeployMu serialises one-click WRT deploys so two concurrent clicks can't
// race the remove/create swap and leave the gateway half-built.
var wrtDeployMu sync.Mutex

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
		// LuCI host port: 0 disables publishing; otherwise it must be a valid
		// TCP port. We avoid the per-user reserved range (<10000) since the
		// gateway is a system container, but we don't reject it outright — the
		// admin may legitimately want a low port (though binding <1024 typically
		// needs root, which the daemon usually has).
		if req.LuCIHostPort < 0 || req.LuCIHostPort > 65535 {
			writeErr(w, http.StatusBadRequest, "invalid luciHostPort (must be 0-65535)")
			return
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

// wrtDeploy is the admin one-click "deploy / re-deploy" handler. It streams
// progress over SSE while it force-rebuilds the WRT gateway: remove any
// existing container → (re)pull the image → create + start → apply UCI config.
// Uses the CURRENT stored policy (no body needed); to change the image or
// addressing first POST /api/wrt, then deploy. Mirrors the image build/import
// SSE pattern (sseSender + sseKeepalive + 15min timeout).
func (a *App) wrtDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	pol, err := a.db.WRTPolicy()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Persist so wrt_policy is the source of truth (also migrates legacy
	// egress_policy forward on first deploy).
	_ = a.db.SaveWRTPolicy(pol)

	dxPol := dockerx.WRTPolicy{
		Enabled:      pol.Enabled,
		Image:        pol.Image,
		LANSubnet:    pol.LANSubnet,
		LANGateway:   pol.LANGateway,
		WANSubnet:    pol.WANSubnet,
		WANGateway:   pol.WANGateway,
		WANIP:        pol.WANIP,
		LuCIHostPort: pol.LuCIHostPort,
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	send := sseSender(w, flusher)
	send("progress", map[string]string{
		"message": "One-click deploy started — force-rebuilding mudp-wrt (image " + pol.Image + "). This will briefly interrupt the gateway.",
	})

	// 15min covers a few-hundred-MB image pull on a slow link; the client can
	// cancel earlier via the job's abort signal (request context cancels).
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()
	sseKeepalive(ctx, send)

	wrtDeployMu.Lock()
	defer wrtDeployMu.Unlock()
	err = a.docker.RecreateWRT(ctx, dxPol, func(line string) {
		send("progress", map[string]string{"message": line})
	})
	if err != nil {
		log.Printf("wrt deploy failed: %v", err)
		send("error", map[string]string{"message": err.Error()})
		return
	}
	a.record(r, "wrt.deploy", "wrt")
	send("done", map[string]string{"image": pol.Image})
}
