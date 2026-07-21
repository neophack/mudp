package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mudp/internal/geoip"
	"mudp/internal/store"
)

func TestMatchAnyCIDR(t *testing.T) {
	cases := []struct {
		name  string
		ip    string
		cidrs []string
		want  bool
	}{
		{"empty list", "1.2.3.4", nil, false},
		{"match in /24", "1.2.3.4", []string{"1.2.3.0/24"}, true},
		{"no match", "1.2.3.4", []string{"1.2.4.0/24"}, false},
		{"bare IP match", "1.2.3.4", []string{"1.2.3.4"}, true},
		{"bare IP no match", "1.2.3.4", []string{"1.2.3.5"}, false},
		{"invalid cidr skipped", "1.2.3.4", []string{"garbage", "1.2.3.0/24"}, true},
		{"multiple, one matches", "10.0.0.5", []string{"192.168.0.0/16", "10.0.0.0/8"}, true},
		{"ipv6", "::1", []string{"::1/128"}, true},
		{"invalid ip", "not-an-ip", []string{"1.2.3.0/24"}, false},
	}
	for _, c := range cases {
		if got := matchAnyCIDR(c.ip, c.cidrs); got != c.want {
			t.Errorf("%s: matchAnyCIDR(%q, %v) = %v, want %v", c.name, c.ip, c.cidrs, got, c.want)
		}
	}
}

func TestIsExemptPath(t *testing.T) {
	exempt := []string{"/healthz", "/readyz", "/metrics", "/api/setup/status", "/api/setup/init"}
	for _, p := range exempt {
		if !isExemptPath(p) {
			t.Errorf("%q should be exempt", p)
		}
	}
	// Login and business endpoints are NOT exempt (region gate applies).
	for _, p := range []string{"/api/login", "/", "/api/containers", "/pan/abc"} {
		if isExemptPath(p) {
			t.Errorf("%q should NOT be exempt", p)
		}
	}
}

func TestCountryAllowed(t *testing.T) {
	p := &store.SecurityPolicy{AllowedCountries: []string{"CN"}}
	if !countryAllowed(geoip.Lookup{Country: "中国"}, p) {
		t.Error("中国 should match CN")
	}
	if countryAllowed(geoip.Lookup{Country: "美国"}, p) {
		t.Error("美国 should NOT match CN")
	}
	// Empty allowlist = no restriction.
	empty := &store.SecurityPolicy{}
	if !countryAllowed(geoip.Lookup{Country: "美国"}, empty) {
		t.Error("empty allowlist should allow all")
	}
}

func TestProvinceAllowed(t *testing.T) {
	p := &store.SecurityPolicy{AllowedCountries: []string{"CN"}, AllowedCNProvinces: []string{"广东省"}}
	// Guangdong allowed.
	if !provinceAllowed(geoip.Lookup{Country: "中国", Province: "广东省"}, p) {
		t.Error("广东省 should be allowed")
	}
	// Suffix-insensitive: admin typed "广东".
	if !provinceAllowed(geoip.Lookup{Country: "中国", Province: "广东省"}, &store.SecurityPolicy{AllowedCNProvinces: []string{"广东"}}) {
		t.Error("广东 should normalize-match 广东省")
	}
	// Beijing not in [广东省].
	if provinceAllowed(geoip.Lookup{Country: "中国", Province: "北京市"}, p) {
		t.Error("北京市 should NOT be allowed when only 广东省 is")
	}
	// Non-China is unaffected by province rule.
	if !provinceAllowed(geoip.Lookup{Country: "美国", Province: "加州"}, p) {
		t.Error("province rule should not affect non-CN countries")
	}
	// Empty province list = all of China allowed.
	allChina := &store.SecurityPolicy{AllowedCountries: []string{"CN"}}
	if !provinceAllowed(geoip.Lookup{Country: "中国", Province: "新疆维吾尔自治区"}, allChina) {
		t.Error("empty province list should allow all CN provinces")
	}
}

func TestNormalizeProvinceStripsSuffix(t *testing.T) {
	if normalizeProvince("广东省") != "广东" {
		t.Errorf("got %q", normalizeProvince("广东省"))
	}
	if normalizeProvince("北京市") != "北京" {
		t.Errorf("got %q", normalizeProvince("北京市"))
	}
	if normalizeProvince("广西壮族自治区") != "广西" {
		t.Errorf("got %q", normalizeProvince("广西壮族自治区"))
	}
	if normalizeProvince("香港特别行政区") != "香港" {
		t.Errorf("got %q", normalizeProvince("香港特别行政区"))
	}
}

// TestGeoGateDisabledPassthrough checks that with the gate disabled (the
// default out-of-the-box), every path is served normally. This is the most
// important property for existing deployments upgrading.
func TestGeoGateDisabledPassthrough(t *testing.T) {
	a := &App{
		loginGuard: nil, // not used when policy disabled
	}
	a.policy.Store(&store.SecurityPolicy{Enabled: false})
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/anything", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("disabled gate: got %d, want 200", rec.Code)
	}
}

// TestGeoGateExemptPathBypassesGate verifies the always-allowed infrastructure
// paths pass even when the gate is enabled.
func TestGeoGateExemptPathBypassesGate(t *testing.T) {
	a := &App{loginGuard: nil, geoip: nil}
	a.policy.Store(&store.SecurityPolicy{Enabled: true, AllowedCountries: []string{"CN"}})
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.9:1234" // would be blocked, but path is exempt
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("exempt /healthz should pass: got %d", rec.Code)
	}
}

// TestGeoGateCIDRAllowOverride exercises the CIDR allowlist override with a
// policy that would otherwise block, but with no GeoIP reader (so region is
// unknown) the CIDR allowlist still admits the IP.
func TestGeoGateCIDRAllowOverride(t *testing.T) {
	a := &App{loginGuard: nil, geoip: nil}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:          true,
		AllowedCountries: []string{"CN"}, // would restrict, but no geoip reader
		AllowedCIDRs:     []string{"10.0.0.0/8"},
	})
	called := false
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "10.0.0.5:1234" // in the allowlist override
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("CIDR allowlist should override and reach handler")
	}
}

// TestGeoGateCIDRBlock always blocks even for an otherwise-allowed region.
func TestGeoGateCIDRBlock(t *testing.T) {
	a := &App{loginGuard: nil, geoip: nil}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:      true,
		BlockedCIDRs: []string{"203.0.113.0/24"},
		AllowedCIDRs: []string{"10.0.0.0/8"},
	})
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("blocked IP should not reach handler")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("blocked CIDR: got %d, want 404", rec.Code)
	}
}

// ---- Cloudflare Tunnel support ----

// TestCFLocationParsesCountry verifies the Cloudflare edge header parser
// extracts the ISO country code and translates CN subdivision codes into the
// Chinese province names the allowlist uses.
func TestCFLocationParsesCountry(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("CF-IPCountry", "US")
	loc, ok := cfLocation(r, "1.2.3.4")
	if !ok || loc.Country != "US" {
		t.Fatalf("US: ok=%v loc=%+v", ok, loc)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("CF-IPCountry", "CN")
	r2.Header.Set("CF-IPRegion", "44") // Guangdong
	r2.Header.Set("CF-IPCity", "Shenzhen")
	loc2, ok := cfLocation(r2, "1.2.3.4")
	if !ok {
		t.Fatal("CN: expected ok")
	}
	if loc2.Province != "广东省" {
		t.Errorf("CF-IPRegion 44 -> province %q, want 广东省", loc2.Province)
	}
	if loc2.City != "Shenzhen" {
		t.Errorf("CF-IPCity -> %q, want Shenzhen", loc2.City)
	}
}

func TestCFLocationAbsentOrUnknown(t *testing.T) {
	// No CF headers → not a CF request.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := cfLocation(r, "1.2.3.4"); ok {
		t.Error("absent CF-IPCountry should report not-ok")
	}
	// CF uses "XX" for unknown origin; treat as no info.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("CF-IPCountry", "XX")
	if _, ok := cfLocation(r2, "1.2.3.4"); ok {
		t.Error("CF-IPCountry=XX should report not-ok")
	}
}

// TestLookupLocationPrefersCF ensures CF headers win over the local GeoIP DB,
// which is the whole point of honoring them (global accuracy vs CN-focused DB).
func TestLookupLocationPrefersCF(t *testing.T) {
	// Build an App with a real embedded GeoIP reader; a CN IP would resolve to
	// 中国 via the DB, but CF-IPCountry=US must override it.
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{geoip: reader}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("CF-IPCountry", "US")
	// 114.114.114.114 is a known CN IP — DB alone would say CN.
	loc, located, noSource := a.lookupLocation(r, "114.114.114.114")
	if !located {
		t.Fatalf("expected lookup ok (noSource=%v)", noSource)
	}
	if loc.Country != "US" {
		t.Errorf("CF override: country=%q, want US", loc.Country)
	}
}

// TestLookupLocationFallsBackToDB verifies that without CF headers, the local
// DB is consulted.
func TestLookupLocationFallsBackToDB(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{geoip: reader}
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no CF headers
	loc, located, _ := a.lookupLocation(r, "114.114.114.114")
	if !located {
		t.Fatal("expected fallback to DB to succeed")
	}
	if loc.Country == "" {
		t.Error("expected non-empty country from DB fallback")
	}
}

// TestLookupLocationNoSourceFailOpen verifies that when NO geo source is
// available (no reader, no CF headers) lookupLocation reports located=false
// with noSource=true — the box is misconfigured, callers fail open.
func TestLookupLocationNoSourceFailOpen(t *testing.T) {
	a := &App{geoip: nil} // no reader
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, located, noSource := a.lookupLocation(r, "203.0.113.1")
	if located {
		t.Error("expected located=false when no geo source available")
	}
	if !noSource {
		t.Error("expected noSource=true when reader is absent")
	}
}

// TestLookupLocationIPv6Unresolvable verifies that a global IPv6 address is
// reported as located=false but noSource=false (we HAVE a reader, it just
// cannot resolve this address) — so a region gate can fail closed instead of
// being bypassable over IPv6.
func TestLookupLocationIPv6Unresolvable(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{geoip: reader}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, located, noSource := a.lookupLocation(r, "2001:4860:4860::8888")
	if located {
		t.Error("expected located=false for global IPv6 (IPv4-only DB)")
	}
	if noSource {
		t.Error("expected noSource=false: reader IS present, just cannot resolve IPv6")
	}
}

// TestGeoGateIPv6FailClosedRegionRule is the regression test for the original
// bug: with a region rule active and a global IPv6 client (which the IPv4-only
// DB cannot locate), the gate MUST block — otherwise "only Guangdong" is
// bypassable just by connecting over IPv6.
func TestGeoGateIPv6FailClosedRegionRule(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{loginGuard: nil, geoip: reader}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:          true,
		AllowedCountries: []string{"CN"},
	})
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("global IPv6 with active region rule should be blocked")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "[2001:4860:4860:0:0:0:0:8888]:54321"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("IPv6 + active region rule: got %d, want 404", rec.Code)
	}
}

// TestGeoGateIPv6FailOpenWhenNoRegionRule verifies that a global IPv6 is still
// admitted when NO region rule is active (admin uses only CIDR rules or the
// login guard). Blocking it would lock out IPv6 users for no benefit.
func TestGeoGateIPv6FailOpenWhenNoRegionRule(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{loginGuard: nil, geoip: reader}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:           true,
		AllowedCIDRs:      []string{"10.0.0.0/8"},
		LoginGuardEnabled: true,
		// no AllowedCountries / AllowedCNProvinces → regionPolicyActive=false
	})
	called := false
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "[2001:4860:4860:0:0:0:0:8888]:54321"
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("IPv6 with no region rule should pass through")
	}
}

// TestGeoGateIPv6FailOpenOptIn verifies the IPv6FailOpen escape hatch: even
// with an active region rule, an admin who opts in admits un-locatable IPv6.
func TestGeoGateIPv6FailOpenOptIn(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{loginGuard: nil, geoip: reader}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:          true,
		AllowedCountries: []string{"CN"},
		IPv6FailOpen:     true,
	})
	called := false
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "[2001:4860:4860:0:0:0:0:8888]:54321"
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("IPv6FailOpen=true should admit global IPv6 even under a region rule")
	}
}

// TestGeoGateIPv6PrivateAdmitted verifies loopback/ULA IPv6 are treated as
// private (trusted), so the admin's own IPv6 LAN connection is not blocked.
func TestGeoGateIPv6PrivateAdmitted(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{loginGuard: nil, geoip: reader}
	a.policy.Store(&store.SecurityPolicy{
		Enabled:          true,
		AllowedCountries: []string{"CN"},
	})
	called := false
	handler := a.geoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "[::1]:54321" // loopback IPv6
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("loopback IPv6 should be treated as private and admitted")
	}
}

// TestSelfCheckIPv6 verifies selfCheck surfaces the ipv6-unresolvable verdict
// under an active region rule, so the save-time lockout guard fires and tells
// the admin to add their IPv6 to the allowlist.
func TestSelfCheckIPv6(t *testing.T) {
	reader, err := geoip.Open()
	if err != nil {
		t.Fatalf("geoip.Open: %v", err)
	}
	a := &App{geoip: reader}
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	// Region rule active + IPv6 → blocked, with the ipv6-unresolvable label.
	region, allowed := a.selfCheck(r, "2001:4860:4860::8888",
		store.SecurityPolicy{Enabled: true, AllowedCountries: []string{"CN"}})
	if allowed {
		t.Error("expected selfCheck to block IPv6 under active region rule")
	}
	if region != "ipv6-unresolvable" {
		t.Errorf("region = %q, want ipv6-unresolvable", region)
	}

	// Same rule but opt-in → admitted with ipv6-fail-open label.
	region, allowed = a.selfCheck(r, "2001:4860:4860::8888",
		store.SecurityPolicy{Enabled: true, AllowedCountries: []string{"CN"}, IPv6FailOpen: true})
	if !allowed {
		t.Error("IPv6FailOpen should admit IPv6 in selfCheck")
	}
	if region != "ipv6-fail-open" {
		t.Errorf("region = %q, want ipv6-fail-open", region)
	}

	// No region rule → IPv6 passes as geoip-unavailable (no restriction to apply).
	region, allowed = a.selfCheck(r, "2001:4860:4860::8888",
		store.SecurityPolicy{Enabled: true})
	if !allowed {
		t.Error("IPv6 with no region rule should be allowed")
	}
}

func TestRegionPolicyActive(t *testing.T) {
	if regionPolicyActive(&store.SecurityPolicy{}) {
		t.Error("empty policy should not be region-active")
	}
	if !regionPolicyActive(&store.SecurityPolicy{AllowedCountries: []string{"CN"}}) {
		t.Error("AllowedCountries set should be region-active")
	}
	if !regionPolicyActive(&store.SecurityPolicy{AllowedCNProvinces: []string{"广东省"}}) {
		t.Error("AllowedCNProvinces set should be region-active")
	}
	// CIDR-only policy is NOT region-active: IPv6 unresolvable IPs pass.
	if regionPolicyActive(&store.SecurityPolicy{BlockedCIDRs: []string{"1.2.3.0/24"}}) {
		t.Error("CIDR-only policy should not be region-active")
	}
}
