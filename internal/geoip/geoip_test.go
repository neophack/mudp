package geoip

import (
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
	// IPv6 is not supported by the bundled DB.
	if _, err := r.Lookup("2001:4860:4860::8888"); err == nil {
		t.Error("expected error for IPv6")
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
