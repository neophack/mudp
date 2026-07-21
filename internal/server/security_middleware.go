package server

import (
	"net"
	"net/http"
	"strings"

	"mudp/internal/geoip"
	"mudp/internal/httpx"
	"mudp/internal/security"
	"mudp/internal/store"
)

// applySecurityPolicy atomically swaps the active security policy and refreshes
// the login guard's config. Called at boot and after each admin save.
func (a *App) applySecurityPolicy(p store.SecurityPolicy) {
	a.policy.Store(&p)
	cfg := security.DefaultConfig()
	cfg.Enabled = p.LoginGuardEnabled
	a.loginGuard.UpdateConfig(cfg)
}

// clientIP is the in-server wrapper over httpx.ClientIP using the app's
// resolved trusted-proxy list.
func (a *App) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, a.trustedProxies)
}

// geoGate is the site-wide region/CIDR gate. Mounted at the router root (after
// recoverPanic and RequestLogger, before any auth group) so it screens every
// path. Non-allowlisted origins get a flat 404 — by design, so a scanner sees
// nothing to fingerprint. Health probes and the first-run setup endpoints are
// always allowed: blocking /healthz would break load balancers, and blocking
// /api/setup/* would brick a fresh deploy.
func (a *App) geoGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		p := a.policy.Load()
		if p == nil || !p.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		ip := a.clientIP(r)

		// CIDR rules take precedence over GeoIP. Block first (deny wins), then
		// explicit allow (the "don't lock myself out" override).
		if matchAnyCIDR(ip, p.BlockedCIDRs) {
			http.NotFound(w, r)
			return
		}
		if matchAnyCIDR(ip, p.AllowedCIDRs) {
			next.ServeHTTP(w, r)
			return
		}

		// No GeoIP reader → fail closed for region rules would lock everyone
		// out on a misconfigured box; instead we fail open here and rely on the
		// explicit CIDR rules above for protection. The admin sees the boot
		// warning that GeoIP is disabled.
		if a.geoip == nil {
			next.ServeHTTP(w, r)
			return
		}
		loc, err := a.geoip.Lookup(ip)
		if err != nil {
			// Unresolvable (private/loopback already mapped to "private", but
			// malformed upstream IPs land here): allow through, since the
			// trusted-proxy chain has already been validated by clientIP.
			next.ServeHTTP(w, r)
			return
		}
		if loc.IsPrivate() {
			next.ServeHTTP(w, r)
			return
		}
		if !countryAllowed(loc, p) || !provinceAllowed(loc, p) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isExemptPath lists paths that bypass the geo gate. Keep this small — every
// entry is a path a region-blocked scanner could still probe.
//
// Only infrastructure that must stay reachable for the machine itself is
// exempt: health probes (a load balancer needs them) and first-run setup
// (blocking it would brick a fresh deploy before an admin can log in to
// configure a CIDR override). Everything else — including /api/login — is
// gated, matching the operator requirement that disallowed regions see a 404
// on the whole site.
func isExemptPath(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics",
		"/api/setup/status", "/api/setup/init":
		return true
	}
	return false
}

// countryAllowed reports whether loc's country is in the allowlist. An empty
// AllowedCountries list means "no country restriction" (the admin is using
// CIDR or province rules instead, or just the login guard).
func countryAllowed(loc geoip.Lookup, p *store.SecurityPolicy) bool {
	if len(p.AllowedCountries) == 0 {
		return true
	}
	code := geoip.CountryCodeOf(loc.Country)
	for _, want := range p.AllowedCountries {
		if strings.EqualFold(code, want) || strings.EqualFold(loc.Country, want) {
			return true
		}
	}
	return false
}

// provinceAllowed further restricts Chinese IPs to specific provinces. Only
// consulted when CN is allowed (so a non-China allowlist ignores provinces);
// an empty province list means "all of China".
func provinceAllowed(loc geoip.Lookup, p *store.SecurityPolicy) bool {
	if len(p.AllowedCNProvinces) == 0 {
		return true
	}
	// Province restriction only applies to CN; if the country isn't CN the
	// country check above already handled it.
	if !isChina(loc) {
		return true
	}
	for _, want := range p.AllowedCNProvinces {
		if normalizeProvince(loc.Province) == normalizeProvince(want) {
			return true
		}
	}
	return false
}

func isChina(loc geoip.Lookup) bool {
	if loc.Country == "中国" {
		return true
	}
	if code := geoip.CountryCodeOf(loc.Country); code == "CN" {
		return true
	}
	return false
}

// provinceSuffixes lists the Chinese administrative suffixes, longest first
// so TrimSuffix matches the most specific (e.g. "壮族自治区" before "自治区" —
// otherwise 广西壮族自治区 would wrongly become 广西壮族).
var provinceSuffixes = []string{
	"壮族自治区",
	"回族自治区",
	"维吾尔自治区",
	"特别行政区",
	"自治区",
	"省",
	"市",
}

// normalizeProvince strips an administrative suffix so an admin typing "广东"
// matches "广东省" in the DB. Best-effort; exact match still works when input
// already carries no suffix.
func normalizeProvince(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range provinceSuffixes {
		s = strings.TrimSuffix(s, suf)
	}
	return s
}

// matchAnyCIDR reports whether ip falls in any of the CIDR strings. Invalid
// CIDR strings in the policy are skipped (they were validated on save, but we
// guard here too). A bare IP is treated as /32 (v4) or /128 (v6).
func matchAnyCIDR(ipStr string, cidrs []string) bool {
	if len(cidrs) == 0 {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(c)
		if err != nil {
			// Bare IP → single-host network.
			if single := net.ParseIP(c); single != nil {
				mask := "32"
				if single.To4() == nil {
					mask = "128"
				}
				if _, n, err := net.ParseCIDR(c + "/" + mask); err == nil {
					if n.Contains(ip) {
						return true
					}
				}
			}
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}
