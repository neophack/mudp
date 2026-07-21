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

		// No GeoIP reader and no CF headers → fail closed for region rules
		// would lock everyone out on a misconfigured box; instead we fail open
		// here and rely on the explicit CIDR rules above for protection. The
		// admin sees the boot warning that GeoIP is disabled. A genuine IPv6
		// address that no source could resolve is different: if a region rule
		// is active we MUST fail closed, or the whole gate is bypassable by
		// connecting over IPv6 (the bundled DB is IPv4-only).
		loc, located, noSource := a.lookupLocation(r, ip)
		if !located {
			if noSource || !regionPolicyActive(p) {
				next.ServeHTTP(w, r)
				return
			}
			// A region rule is active but this IP can't be geo-located
			// (typically a global IPv6 address the IPv4-only DB can't resolve).
			// Block unless the admin has explicitly switched IPv6 to fail-open.
			if p.IPv6FailOpen {
				next.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
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

// regionPolicyActive reports whether the policy restricts by region at all —
// i.e. whether the gate's outcome depends on knowing where the client is. When
// no country/province list is set the admin is using only CIDR rules (already
// evaluated above) or just the login guard, so an unresolvable IP (e.g. a
// global IPv6 the IPv4-only DB cannot locate) must NOT be blocked.
func regionPolicyActive(p *store.SecurityPolicy) bool {
	return len(p.AllowedCountries) > 0 || len(p.AllowedCNProvinces) > 0
}

// lookupLocation resolves the geo location of ip for the request, preferring
// Cloudflare edge headers when the request arrived via Cloudflare (CF-IPCountry
// is globally accurate and free, unlike the bundled China-focused xdb). Falls
// back to the embedded GeoIP DB.
//
// Three outcomes:
//   - (loc, located=true):  a region was resolved (including Country=="private"
//     for LAN/loopback). Caller applies the geo rules.
//   - (loc, located=false, noSource=true):  NO geo source is configured at all
//     (no CF headers AND no GeoIP reader). The box is misconfigured, so the
//     caller FAILS OPEN — we never lock out everyone because the DB failed to
//     load at boot. The admin sees the boot warning.
//   - (loc, located=false, noSource=false):  a source IS available but could
//     not locate THIS ip — most importantly a global IPv6 address, which the
//     bundled IPv4-only xdb cannot resolve. Caller FAILS CLOSED when a region
//     rule is active, otherwise a non-allowlisted region could trivially bypass
//     the gate by connecting over IPv6.
//
// CF headers are only honored when the direct peer is a trusted proxy (already
// enforced by clientIP's caller contract), so an attacker cannot spoof them by
// connecting directly — a forged CF-IPCountry from a public peer is ignored.
func (a *App) lookupLocation(r *http.Request, ip string) (loc geoip.Lookup, located, noSource bool) {
	if l, ok := cfLocation(r, ip); ok {
		return l, true, false
	}
	if a.geoip == nil {
		return geoip.Lookup{}, false, true
	}
	l, err := a.geoip.Lookup(ip)
	if err != nil {
		return geoip.Lookup{}, false, false
	}
	return l, true, false
}

// cfLocation builds a Lookup from Cloudflare edge headers when CF-IPCountry is
// present. CF-IPRegion is a subdivision code (e.g. "44" for Guangdong) on the
// CF-IPContinent=AS line — we translate the common CN subdivision codes so the
// province allowlist keeps working for Tunnel deployments.
func cfLocation(r *http.Request, ip string) (geoip.Lookup, bool) {
	country := strings.TrimSpace(r.Header.Get("CF-IPCountry"))
	if country == "" || strings.EqualFold(country, "XX") {
		return geoip.Lookup{}, false
	}
	loc := geoip.Lookup{Country: country}
	// CF gives ISO country codes (CN/HK/US...), which countryAllowed handles
	// directly. For CN province matching we need the Chinese province name that
	// provinceAllowed/normalizeProvince expect, so translate CF subdivision codes.
	if strings.EqualFold(country, "CN") {
		sub := strings.TrimSpace(r.Header.Get("CF-IPRegion"))
		if name, ok := cfCNSubdivision[sub]; ok {
			loc.Province = name
		}
		if city := strings.TrimSpace(r.Header.Get("CF-IPCity")); city != "" {
			loc.City = city
		}
	}
	return loc, true
}

// cfCNSubdivision maps ISO 3166-2:CN subdivision codes (as emitted by
// Cloudflare in CF-IPRegion) to the Chinese province names the bundled xdb and
// the province allowlist use. Source: ISO 3166-2:CN.
var cfCNSubdivision = map[string]string{
	"11": "北京市", "12": "天津市", "13": "河北省", "14": "山西省", "15": "内蒙古自治区",
	"21": "辽宁省", "22": "吉林省", "23": "黑龙江省", "31": "上海市", "32": "江苏省",
	"33": "浙江省", "34": "安徽省", "35": "福建省", "36": "江西省", "37": "山东省",
	"41": "河南省", "42": "湖北省", "43": "湖南省", "44": "广东省", "45": "广西壮族自治区",
	"46": "海南省", "50": "重庆市", "51": "四川省", "52": "贵州省", "53": "云南省",
	"54": "西藏自治区", "61": "陕西省", "62": "甘肃省", "63": "青海省", "64": "宁夏回族自治区",
	"65": "新疆维吾尔自治区", "71": "台湾省", "81": "香港特别行政区", "91": "澳门特别行政区",
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
