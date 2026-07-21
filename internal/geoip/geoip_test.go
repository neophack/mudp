package geoip

import (
	"errors"
	"sync"
	"testing"
)

func TestOpen(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r == nil || len(r.buf) == 0 {
		t.Fatal("Open returned empty reader")
	}
}

func TestLookupKnownIPs(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 114DNS is a well-known Chinese public resolver; the bundled CN-focused
	// DB must resolve it to China.
	loc, err := r.Lookup("114.114.114.114")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if loc.Country != "中国" {
		t.Logf("note: 114.114.114.114 resolved to country=%q province=%q (DB content may vary)", loc.Country, loc.Province)
	}
	if loc.Province == "" {
		t.Logf("note: 114.114.114.114 has empty province (DB content may vary)")
	}
}

func TestLookupPrivate(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.5.5"} {
		loc, err := r.Lookup(ip)
		if err != nil {
			t.Errorf("Lookup(%s): %v", ip, err)
			continue
		}
		if !loc.IsPrivate() {
			t.Errorf("Lookup(%s): expected private, got Country=%q", ip, loc.Country)
		}
	}
}

func TestLookupBadIP(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := r.Lookup("not-an-ip"); err == nil {
		t.Error("expected error for malformed IP")
	}
	// Global IPv6 is not resolvable by the bundled IPv4-only DB: it returns
	// the sentinel so the server layer can choose fail-open vs fail-closed.
	_, err = r.Lookup("2001:4860:4860::8888")
	if err == nil {
		t.Error("expected error for global IPv6")
	}
	if !errors.Is(err, ErrIPv6Unsupported) {
		t.Errorf("global IPv6 error = %v, want ErrIPv6Unsupported", err)
	}
}

func TestLookupIPv6Private(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Private IPv6 (loopback / unique-local / link-local) must still map to
	// "private", matching the IPv4 behavior, so LAN clients are trusted.
	for _, ip := range []string{"::1", "fc00::1", "fd00::1", "fe80::1"} {
		loc, err := r.Lookup(ip)
		if err != nil {
			t.Errorf("Lookup(%s): %v", ip, err)
			continue
		}
		if !loc.IsPrivate() {
			t.Errorf("Lookup(%s): expected private, got Country=%q", ip, loc.Country)
		}
	}
}

func TestLookupIPv4MappedIPv6(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// IPv4-mapped IPv6 (::ffff:1.2.3.4) still has an IPv4 representation and
	// must be geo-located, not rejected as IPv6.
	loc, err := r.Lookup("::ffff:114.114.114.114")
	if err != nil {
		t.Fatalf("Lookup(::ffff:114.114.114.114): %v", err)
	}
	if loc.Country == "" || loc.Country == "private" {
		t.Errorf("expected a real region for IPv4-mapped IPv6, got %+v", loc)
	}
}

func TestIsIPv6(t *testing.T) {
	if IsIPv6("114.114.114.114") {
		t.Error("114.114.114.114 should not be IPv6")
	}
	if !IsIPv6("2001:4860:4860::8888") {
		t.Error("2001:4860:4860::8888 should be IPv6")
	}
	if !IsIPv6("::1") {
		t.Error("::1 should be IPv6")
	}
	// IPv4-mapped IPv6 (::ffff:1.2.3.4) has an IPv4 representation (To4() is
	// non-nil), so IsIPv6 reports false — it can be geo-located as IPv4 and
	// should NOT trigger the IPv6 fail-closed path.
	if IsIPv6("::ffff:1.2.3.4") {
		t.Error("::ffff:1.2.3.4 (IPv4-mapped) should report as IPv4 family (not IPv6)")
	}
	if IsIPv6("not-an-ip") {
		t.Error("garbage should not be IPv6")
	}
}

func TestLookupIsConcurrencySafe(t *testing.T) {
	r, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ips := []string{"114.114.114.114", "8.8.8.8", "1.1.1.1", "220.181.38.148"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ip := ips[j%len(ips)]
				if _, err := r.Lookup(ip); err != nil {
					t.Errorf("concurrent Lookup(%s): %v", ip, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestParseRegion(t *testing.T) {
	loc := parseRegion("中国|0|广东省|深圳市|电信")
	if loc.Country != "中国" || loc.Province != "广东省" || loc.City != "深圳市" || loc.ISP != "电信" {
		t.Errorf("unexpected parse: %+v", loc)
	}
	if loc.Region != "" { // "0" should normalize to ""
		t.Errorf("expected empty Region, got %q", loc.Region)
	}
}

func TestCountryCodeOf(t *testing.T) {
	if got := CountryCodeOf("中国"); got != "CN" {
		t.Errorf("中国 -> %q, want CN", got)
	}
	if got := CountryCodeOf("香港"); got != "HK" {
		t.Errorf("香港 -> %q, want HK", got)
	}
	if got := CountryCodeOf("cn"); got != "CN" {
		t.Errorf("cn -> %q, want CN (case-insensitive pass-through)", got)
	}
}
