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
