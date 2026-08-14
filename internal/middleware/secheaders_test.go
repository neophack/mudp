package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// baselineCSP is the exact policy served when no IP worker origin is set.
const baselineCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' ws: wss: https://api.ipify.org https://api64.ipify.org https://ifconfig.me; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// setWorkerOrigin swaps the package-global worker origin and restores the old
// value when the test ends, keeping these tests order-independent. The global
// is not parallel-safe, so no test in this file calls t.Parallel().
func setWorkerOrigin(t *testing.T, origin string) {
	t.Helper()
	cspMu.RLock()
	old := cspConnectExtra
	cspMu.RUnlock()
	SetIPWorkerOrigin(origin)
	t.Cleanup(func() {
		cspMu.Lock()
		cspConnectExtra = old
		cspMu.Unlock()
	})
}

func TestSecurityHeadersPlainHTTP(t *testing.T) {
	setWorkerOrigin(t, "") // guarantee the baseline CSP regardless of test order
	handler := SecurityHeaders(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
		"Content-Security-Policy": baselineCSP,
	}
	for name, v := range want {
		if got := rec.Header().Get(name); got != v {
			t.Errorf("%s = %q, want %q", name, got, v)
		}
	}
	// HSTS on plain HTTP would pin a host that may not serve HTTPS yet.
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want absent on plain HTTP", got)
	}
}

func TestSecurityHeadersHSTSOnSecureRequest(t *testing.T) {
	// httptest.NewRequest's default RemoteAddr peer; trusting it lets the
	// X-Forwarded-Proto cases model a configured reverse proxy.
	peerTrusted, err := ParseTrustedProxies("192.0.2.1")
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ok := func(trusted *TrustedProxies) http.Handler {
		return SecurityHeaders(trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}
	cases := []struct {
		name    string
		trusted *TrustedProxies
		secure  func(*http.Request)
		hsts    bool
	}{
		{"TLS connection", nil, func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, true},
		{"X-Forwarded-Proto https from trusted proxy", peerTrusted, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"X-Forwarded-Proto case-insensitive from trusted proxy", peerTrusted, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "HTTPS") }, true},
		// docs/SECURITY-AUDIT.md L-5: a DIRECT client forging the header must
		// not earn HSTS on a plaintext response — that would pin a host that
		// may not serve HTTPS yet.
		{"X-Forwarded-Proto https from untrusted peer (spoofed)", nil, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		c.secure(req)
		rec := httptest.NewRecorder()
		ok(c.trusted).ServeHTTP(rec, req)
		got := rec.Header().Get("Strict-Transport-Security")
		if c.hsts && got != "max-age=31536000; includeSubDomains" {
			t.Errorf("%s: Strict-Transport-Security = %q, want the HSTS header", c.name, got)
		}
		if !c.hsts && got != "" {
			t.Errorf("%s: Strict-Transport-Security = %q, want absent", c.name, got)
		}
	}
}

// Handlers serving user-supplied file bodies choose their own stricter CSP;
// the middleware's policy must not be what the response ends up carrying.
func TestSecurityHeadersDoesNotClobberHandlerCSP(t *testing.T) {
	const handlerCSP = "default-src 'none'; sandbox"
	handler := SecurityHeaders(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", handlerCSP)
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != handlerCSP {
		t.Errorf("Content-Security-Policy = %q, want the handler's %q", got, handlerCSP)
	}
}

func TestSanitisedIPWorkerOrigin(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bare host", "https://worker.example.com", "https://worker.example.com"},
		{"host with port", "https://worker.example.com:8443", "https://worker.example.com:8443"},
		{"surrounding whitespace trimmed", "  https://worker.example.com\n", "https://worker.example.com"},
		{"empty", "", ""},
		{"scheme only", "https://", ""},
		{"http rejected", "http://worker.example.com", ""},
		{"javascript scheme", "javascript:alert(1)", ""},
		{"path rejected", "https://worker.example.com/hook", ""},
		{"query rejected", "https://worker.example.com?x=1", ""},
		{"fragment rejected", "https://worker.example.com#x", ""},
		{"semicolon directive injection", "https://worker.example.com; script-src *", ""},
		{"double quote breakout", `https://worker.example.com" onload="x`, ""},
		{"single quote", "https://worker.example.com/x'y", ""},
		{"embedded space", "https://worker .example.com", ""},
		{"embedded tab", "https://worker\t.example.com", ""},
		{"embedded newline", "https://worker.example.com\nhttps://evil.example.com", ""},
		{"comma separator", "https://a.example.com,https://b.example.com", ""},
		{"angle brackets", "https://<script>.example.com", ""},
	}
	for _, c := range cases {
		if got := sanitisedIPWorkerOrigin(c.in); got != c.want {
			t.Errorf("%s: sanitisedIPWorkerOrigin(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestBuildCSPIncludesSanitisedWorkerOrigin(t *testing.T) {
	setWorkerOrigin(t, "https://worker.example.com:8443")
	csp := buildCSP()
	want := "connect-src 'self' ws: wss: https://api.ipify.org https://api64.ipify.org https://ifconfig.me https://worker.example.com:8443;"
	if !strings.Contains(csp, want) {
		t.Errorf("buildCSP() missing worker origin in connect-src\n got: %s\nwant substring: %s", csp, want)
	}
}

// The injection guard: an operator-supplied origin containing CSP metacharacters
// must never reach the header — SetIPWorkerOrigin drops it outright, leaving the
// policy exactly at the baseline.
func TestBuildCSPNeverContainsMaliciousOrigin(t *testing.T) {
	setWorkerOrigin(t, "https://evil.example.com; script-src *")
	csp := buildCSP()
	if strings.Contains(csp, "evil.example.com") {
		t.Errorf("malicious origin leaked into CSP: %s", csp)
	}
	if csp != baselineCSP {
		t.Errorf("buildCSP() = %q, want the untouched baseline %q", csp, baselineCSP)
	}
}
