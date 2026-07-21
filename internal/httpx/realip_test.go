package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPNoProxyPeer(t *testing.T) {
	// A public client (203.0.113.5) sending XFF must NOT have its header
	// honored — it could be forged.
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("X-Forwarded-For", "10.0.0.99")
	if got := ClientIP(r, trusted); got != "203.0.113.5" {
		t.Errorf("untrusted peer: got %q, want 203.0.113.5", got)
	}
}

func TestClientIPTrustedLoopback(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("X-Forwarded-For", "114.114.114.114")
	if got := ClientIP(r, trusted); got != "114.114.114.114" {
		t.Errorf("loopback+XFF: got %q, want 114.114.114.114", got)
	}
}

func TestClientIPTrustedLAN(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:54321"
	r.Header.Set("X-Real-IP", "8.8.8.8")
	if got := ClientIP(r, trusted); got != "8.8.8.8" {
		t.Errorf("LAN+X-Real-IP: got %q, want 8.8.8.8", got)
	}
}

func TestClientIPCFConnectingPrecedence(t *testing.T) {
	// CF-Connecting-IP should win over XFF and X-Real-IP.
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.1:54321"
	r.Header.Set("CF-Connecting-IP", "1.2.3.4")
	r.Header.Set("X-Real-IP", "9.9.9.9")
	r.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := ClientIP(r, trusted); got != "1.2.3.4" {
		t.Errorf("CF precedence: got %q, want 1.2.3.4", got)
	}
}

func TestClientIPXFFLeftMost(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1, 10.0.0.2")
	if got := ClientIP(r, trusted); got != "203.0.113.9" {
		t.Errorf("XFF left-most: got %q, want 203.0.113.9", got)
	}
}

func TestClientIPGarbageXFF(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:54321"
	r.Header.Set("X-Forwarded-For", "not-an-ip, , 8.8.8.8")
	if got := ClientIP(r, trusted); got != "8.8.8.8" {
		t.Errorf("XFF skip-garbage: got %q, want 8.8.8.8", got)
	}
}

func TestClientIPNoHeadersFallsBackToPeer(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:54321"
	if got := ClientIP(r, trusted); got != "10.0.0.2" {
		t.Errorf("no headers: got %q, want 10.0.0.2", got)
	}
}

func TestClientIPConfiguredProxyOnly(t *testing.T) {
	// Only 10.0.0.0/24 trusted; a 192.168.x peer should be ignored.
	trusted := ParseCIDRs("10.0.0.0/24")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.0.5:54321"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := ClientIP(r, trusted); got != "192.168.0.5" {
		t.Errorf("non-trusted LAN: got %q, want 192.168.0.5", got)
	}
	// And 10.0.0.5 inside the configured range is honored.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "10.0.0.5:54321"
	r2.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := ClientIP(r2, trusted); got != "1.1.1.1" {
		t.Errorf("configured proxy: got %q, want 1.1.1.1", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	// Invalid tokens are silently dropped (startup config must not abort boot
	// on a single typo); the rest are parsed.
	nets := ParseCIDRs("10.0.0.0/8, 192.168.0.5, bad-input, fc00::/7")
	if len(nets) != 3 {
		t.Fatalf("expected 3 nets (bad-input dropped), got %d", len(nets))
	}
	// Bare IP should be promoted to /32.
	ipNet := nets[1]
	if ipNet.String() != "192.168.0.5/32" {
		t.Errorf("bare IP promotion: got %q, want 192.168.0.5/32", ipNet.String())
	}
	if ParseCIDRs("") != nil {
		t.Error("empty input should return nil")
	}
	// All-invalid input returns an empty (non-nil only if at least one valid).
	if got := ParseCIDRs("only-garbage"); len(got) != 0 {
		t.Errorf("all-invalid input: got %d nets, want 0", len(got))
	}
}

func TestClientIPMalformedRemoteAddr(t *testing.T) {
	trusted := DefaultTrustedProxies()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "no-port-here"
	// Should not panic and should return the raw value.
	got := ClientIP(r, trusted)
	if got == "" {
		t.Error("expected non-empty fallback for malformed RemoteAddr")
	}
}
