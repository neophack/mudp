package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mudp/internal/auth"
	"mudp/internal/httpx"
)

func TestRecoverPanic(t *testing.T) {
	handler := RecoverPanic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}

func TestRequestLoggerSetsRequestID(t *testing.T) {
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := httpx.RequestID(r)
		if id == "" {
			t.Error("request ID missing")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("response missing X-Request-ID")
	}
}

func TestRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(1, 1, 0)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request allowed.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request code = %d", rec1.Code)
	}

	// Immediate second request blocked.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code = %d, want 429", rec2.Code)
	}
}

func TestCSRFProtectSafeMethods(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rec.Code)
	}
}

func TestCSRFProtectBlocksMissingToken(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestCSRFTokenRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	token, err := CSRFToken(rec, req)
	if err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != csrfCookieName {
		t.Fatalf("CSRF cookie not set: %+v", cookies)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(cookies[0])
	req.Header.Set(csrfHeaderName, token)

	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
}

// TestCSRFCookieOutlivesTheBrowserSession guards the regression where the CSRF
// cookie had no expiry: it was dropped when the browser closed while the 24h
// session cookie survived, so the user returned still logged in and every
// state-changing request was rejected for a missing CSRF token.
func TestCSRFCookieOutlivesTheBrowserSession(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := CSRFToken(rec, httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie set")
	}
	c := cookies[0]
	if c.MaxAge <= 0 && c.Expires.IsZero() {
		t.Fatal("CSRF cookie has no expiry, so it dies with the browser session")
	}
	if want := int(auth.SessionTTL / time.Second); c.MaxAge != want {
		t.Errorf("MaxAge = %d, want %d (the session cookie's lifetime)", c.MaxAge, want)
	}
}

// TestClientIPIgnoresUntrustedForwardedFor is the anti-bypass case: if the
// forwarding header were believed unconditionally, an attacker could send a
// different X-Forwarded-For on every login attempt and get a fresh rate-limit
// bucket each time.
func TestClientIPIgnoresUntrustedForwardedFor(t *testing.T) {
	tp, err := ParseTrustedProxies("")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := tp.ClientIP(req); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the socket peer 203.0.113.9", got)
	}
}

func TestClientIPUsesForwardedForFromTrustedProxy(t *testing.T) {
	tp, err := ParseTrustedProxies("10.0.0.0/8, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, remote, xff, want string
	}{
		{"single hop", "10.0.0.1:9000", "198.51.100.7", "198.51.100.7"},
		{"chain ends at proxy", "10.0.0.1:9000", "198.51.100.7, 10.0.0.5", "198.51.100.7"},
		{"spoofed prefix ignored", "10.0.0.1:9000", "1.1.1.1, 198.51.100.7", "198.51.100.7"},
		{"loopback proxy", "127.0.0.1:9000", "198.51.100.7", "198.51.100.7"},
		{"no header falls back to peer", "10.0.0.1:9000", "", "10.0.0.1"},
		{"garbage header falls back", "10.0.0.1:9000", "not-an-ip", "10.0.0.1"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := tp.ClientIP(req); got != c.want {
			t.Errorf("%s: ClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// Separate clients must get separate buckets, or one noisy source locks out
// everyone behind the same proxy.
func TestRateLimiterIsPerClient(t *testing.T) {
	tp, _ := ParseTrustedProxies("10.0.0.0/8")
	limiter := NewRateLimiter(1, 1, 0).TrustProxies(tp)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(client string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:9000"
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first client = %d, want 200", code)
	}
	if code := call("198.51.100.2"); code != http.StatusOK {
		t.Fatalf("second client = %d, want 200 (separate bucket)", code)
	}
	if code := call("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("first client repeat = %d, want 429", code)
	}
}

// Idle buckets must be reclaimed; the map used to grow without bound.
func TestRateLimiterEvictsIdleEntries(t *testing.T) {
	limiter := NewRateLimiter(1, 1, 10*time.Millisecond)
	limiter.getLimiter("1.1.1.1")
	if n := len(limiter.limiters); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
	time.Sleep(20 * time.Millisecond)
	limiter.getLimiter("2.2.2.2") // triggers the sweep
	if _, stale := limiter.limiters["1.1.1.1"]; stale {
		t.Error("idle entry was not evicted")
	}
	if _, fresh := limiter.limiters["2.2.2.2"]; !fresh {
		t.Error("current entry should be retained")
	}
}

func TestParseTrustedProxiesRejectsGarbage(t *testing.T) {
	if _, err := ParseTrustedProxies("not-an-ip"); err == nil {
		t.Error("expected an error for an unparsable entry")
	}
}
