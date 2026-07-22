package server

import (
	"net"
	"net/http"
	"strings"

	"mudp/internal/store"
)

// mcpPolicyResponse is what GET /api/settings/mcp returns. It bundles the
// durable policy with a real-time read of the calling admin's own source IP, so
// the UI can show "you would be allowed" as a self-lockout guard before the
// admin narrows AllowCIDRs and locks themselves out of the SSE port.
type mcpPolicyResponse struct {
	store.MCPPolicy
	MyIP                 string `json:"myIp"`
	MyAllowed            bool   `json:"myAllowed"`
	// WrtRunning reports whether the WRT gateway container is active, so the
	// admin can tell if isolation is fully in force or degraded.
	WrtRunning bool `json:"wrtRunning"`
}

// mcpSettings is the admin GET/POST handler for the MCP/SSE listener policy.
// Mirrors securitySettings: admin gate, decode into the store struct, validate
// every CIDR, persist, hot-reload the listener, audit, and return ok.
func (a *App) mcpSettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		pol, err := a.db.MCPPolicy()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := mcpPolicyResponse{MCPPolicy: pol}
		resp.MyIP = a.clientIP(r)
		allow, _ := a.mcpAllowNets.Load().([]*net.IPNet)
		if ip := net.ParseIP(resp.MyIP); ip != nil {
			resp.MyAllowed = matchAnyIPNet(ip, allow)
		}
		resp.WrtRunning = a.docker.WRTStatus(r.Context()).Running
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		var req store.MCPPolicy
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Validate every CIDR up front so we never persist a malformed rule.
		for _, c := range req.AllowCIDRs {
			if !isValidCIDROrIP(strings.TrimSpace(c)) {
				writeErr(w, http.StatusBadRequest, "invalid CIDR/IP: "+c)
				return
			}
		}
		req.AllowCIDRs = normalizeList(req.AllowCIDRs)
		req.PublicBaseURL = strings.TrimSpace(req.PublicBaseURL)

		// Port range check (0 is allowed and means "assign random on enable").
		if req.Port != 0 && (req.Port < mcpMinPort || req.Port > mcpMaxPort) {
			writeErr(w, http.StatusBadRequest, "port must be in 50000-59999 (or 0 for random)")
			return
		}

		// First enable with no port → assign a random one and persist.
		if req.Enabled && req.Port == 0 {
			req.Port = randomMcpPort()
		}

		// Self-lockout guard: if the listener is enabled and the admin's own
		// source IP wouldn't pass the new AllowCIDRs, refuse the save so they
		// don't lock themselves out of the SSE port.
		if req.Enabled {
			effective := req.AllowCIDRs
			if len(effective) == 0 {
				effective = []string{"127.0.0.0/8", "::1/128"}
			}
			myIP := a.clientIP(r)
			if !matchAnyCIDR(myIP, effective) {
				writeErr(w, http.StatusBadRequest, "拒绝保存：该配置会将你当前的来源排除在 SSE 端口之外。请先把 "+myIP+" 加入 AllowCIDRs，或留空使用默认回环。(This would block your current source from the SSE port — add "+myIP+" to AllowCIDRs first, or leave empty for loopback.)")
				return
			}
		}

		if err := a.db.SaveMCPPolicy(req); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.applyMCPPolicy(req) // hot-reload the source gate + listener
		a.record(r, "mcp.policy.update", "mcp")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
