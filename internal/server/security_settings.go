package server

import (
	"net"
	"net/http"
	"strings"

	"mudp/internal/store"
)

// securityPolicyResponse is what GET /api/settings/security returns. It bundles
// the durable policy with the live lockout snapshot and a real-time read of the
// calling admin's own IP/region, so the UI can show "you would be allowed" as a
// self-lockout guard before the admin saves.
type securityPolicyResponse struct {
	store.SecurityPolicy
	Locked    []lockedEntry `json:"locked"`
	MyIP      string        `json:"myIp"`
	MyRegion  string        `json:"myRegion"`
	MyAllowed bool          `json:"myAllowed"`
}

type lockedEntry struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	LockedUntil string `json:"lockedUntil"`
	Reason      string `json:"reason"`
}

// securitySettings is the admin GET/POST handler for the IP-restriction and
// brute-force policy. The GET also returns the live locked list and a
// self-check for the calling admin's IP, so the UI can warn before a save that
// would lock the admin out.
func (a *App) securitySettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		policy, err := a.db.SecurityPolicy()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := securityPolicyResponse{SecurityPolicy: policy}
		for _, s := range a.loginGuard.LockedStatuses() {
			resp.Locked = append(resp.Locked, lockedEntry{
				Key: s.Key, Kind: s.Kind, LockedUntil: s.LockedUntil.Format("2006-01-02 15:04:05"), Reason: s.Reason,
			})
		}
		// Self-check: where does the calling admin appear to come from?
		resp.MyIP = a.clientIP(r)
		resp.MyRegion, resp.MyAllowed = a.selfCheck(resp.MyIP, policy)
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		var req store.SecurityPolicy
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Validate every CIDR up front so we never persist a malformed rule
		// (which would silently misbehave at match time).
		for _, c := range append(append([]string{}, req.AllowedCIDRs...), req.BlockedCIDRs...) {
			if !isValidCIDROrIP(strings.TrimSpace(c)) {
				writeErr(w, http.StatusBadRequest, "invalid CIDR/IP: "+c)
				return
			}
		}
		// Normalize the allowlists: trim and dedup case-insensitively.
		req.AllowedCountries = normalizeList(req.AllowedCountries)
		req.AllowedCNProvinces = normalizeList(req.AllowedCNProvinces)
		req.AllowedCIDRs = normalizeList(req.AllowedCIDRs)
		req.BlockedCIDRs = normalizeList(req.BlockedCIDRs)

		// Self-lockout guard: if enabling the gate would block the admin's own
		// current IP, refuse the save unless they have explicitly added their IP
		// to the allowlist. This is the single most important safety rail —
		// without it an admin in Guangdong could enable "only Guangdong" while
		// their proxy presents a HK egress IP and lose access.
		if req.Enabled {
			myIP := a.clientIP(r)
			_, allowed := a.selfCheck(myIP, req)
			if !allowed && !matchAnyCIDR(myIP, req.AllowedCIDRs) {
				writeErr(w, http.StatusBadRequest, "拒绝保存：该配置会将你当前的 IP 排除在外。请先把你当前的 IP ("+myIP+") 加入 CIDR 白名单，再开启地区限制。(This policy would block your current IP — add it to the CIDR allowlist first.)")
				return
			}
		}

		if err := a.db.SaveSecurityPolicy(req); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.applySecurityPolicy(req) // hot-reload the gate + login guard
		a.record(r, "security.policy.update", "security")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// selfCheck reports the region label and allow verdict for ip under policy, as
// the live geoGate would compute it. Used by the GET response so the UI shows
// "your region: 广东省 / allowed" without the admin having to test-save.
func (a *App) selfCheck(ip string, p store.SecurityPolicy) (region string, allowed bool) {
	if matchAnyCIDR(ip, p.BlockedCIDRs) {
		return "blocked-CIDR", false
	}
	if matchAnyCIDR(ip, p.AllowedCIDRs) {
		return "allowed-CIDR", true
	}
	if a.geoip == nil {
		return "geoip-unavailable", true
	}
	loc, err := a.geoip.Lookup(ip)
	if err != nil || loc.IsPrivate() {
		if loc.IsPrivate() {
			return "private-network", true
		}
		return "unknown", true
	}
	region = loc.Country
	if loc.Province != "" {
		region += " / " + loc.Province
	}
	if loc.City != "" {
		region += " / " + loc.City
	}
	allowed = countryAllowed(loc, &p) && provinceAllowed(loc, &p)
	return region, allowed
}

// isValidCIDROrIP accepts either "1.2.3.0/24" or a bare "1.2.3.4" (promoted to
// a single-host network at match time).
func isValidCIDROrIP(s string) bool {
	if s == "" {
		return false
	}
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	if ip := net.ParseIP(s); ip != nil {
		return true
	}
	return false
}

// normalizeList trims, drops empties, and dedups case-insensitively while
// preserving order. CIDRs/IPs are kept in their original case (they are
// case-insensitive anyway); province names keep their case for display.
func normalizeList(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}
