package middleware

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	// The panic value must never reach the client; the body is the fixed
	// WriteErr envelope from recover.go, not the panic string.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (body %q)", err, rec.Body.String())
	}
	if body["error"] != "internal server error" {
		t.Errorf("error = %q, want %q", body["error"], "internal server error")
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("response leaks the panic value: %q", rec.Body.String())
	}
}

func TestRequestLoggerSetsRequestID(t *testing.T) {
	var ctxID string
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = httpx.RequestID(r)
		if ctxID == "" {
			t.Error("request ID missing from context")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("response missing X-Request-ID")
	}
	if got := rec.Header().Get("X-Request-ID"); got != ctxID {
		t.Errorf("response X-Request-ID = %q, context request ID = %q", got, ctxID)
	}
}

// A client-supplied X-Request-ID must be passed through unchanged so upstream
// gateways can correlate their logs with ours (logger.go reuses it instead of
// minting a fresh one).
func TestRequestLoggerPassesThroughClientRequestID(t *testing.T) {
	const clientID = "client-id-123"
	var ctxID string
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = httpx.RequestID(r)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", clientID)
	handler.ServeHTTP(rec, req)
	if ctxID != clientID {
		t.Errorf("context request ID = %q, want the client-supplied %q", ctxID, clientID)
	}
	if got := rec.Header().Get("X-Request-ID"); got != clientID {
		t.Errorf("response X-Request-ID = %q, want the client-supplied %q", got, clientID)
	}
}

// redactPath masks capability tokens before they hit the log: /mcp/{token}
// authenticates a session and /pan/{token} grants share access, so either one
// verbatim in a log file is a password in a log file. /mcp/ with no token
// segment is pinned as returned unchanged.
func TestRedactPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/mcp/abc123", "/mcp/[redacted]"},
		{"/pan/tok/x/y", "/pan/[redacted]/x/y"},
		{"/other/tok", "/other/tok"},
		{"/mcp/", "/mcp/"},
	}
	for _, c := range cases {
		if got := redactPath(c.in); got != c.want {
			t.Errorf("redactPath(%q) = %q, want %q", c.in, got, c.want)
		}
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

	// Immediate second request blocked, with a Retry-After telling the client
	// how long to back off (whole seconds, rounded up).
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code = %d, want 429", rec2.Code)
	}
	ra := rec2.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("429 response missing Retry-After")
	}
	if n, err := strconv.Atoi(ra); err != nil || n <= 0 {
		t.Errorf("Retry-After = %q, want a positive integer", ra)
	}
}

// isSafeMethod exempts exactly GET/HEAD/OPTIONS/TRACE, each without a token.
func TestCSRFProtectSafeMethods(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s without a CSRF token: code = %d, want 200 (exempt)", method, rec.Code)
		}
	}
}

// Every method outside the safe list requires the cookie/header pair.
func TestCSRFProtectBlocksMissingToken(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("reached handler without a CSRF token")
		w.WriteHeader(http.StatusOK)
	}))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token: code = %d, want 403", method, rec.Code)
		}
	}
}

// CSRFProtect compares against X-CSRF-Token only (docs/SECURITY-AUDIT.md L-4):
// a valid cookie plus a token arriving via query or form body must still be
// rejected, because those channels leak the token into logs and history.
func TestCSRFProtectRejectsQueryAndFormTokens(t *testing.T) {
	const tok = "tok123"
	cases := []struct {
		name string
		url  string
		body string
	}{
		{"token in query", "/?token=" + tok, ""},
		{"token in form body", "/", "token=" + tok},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, c.url, strings.NewReader(c.body))
		if c.body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: tok})
		rec := httptest.NewRecorder()
		CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("%s: reached handler", c.name)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", c.name, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: body is not JSON: %v", c.name, err)
			continue
		}
		// The cookie is present, so this is a mismatch, not a missing token.
		if body["error"] != "CSRF token mismatch" {
			t.Errorf("%s: error = %q, want %q", c.name, body["error"], "CSRF token mismatch")
		}
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

// CSRFTokenFromRequest reads the token from the cookie only — the header and
// form/query paths are extraction points for CSRFProtect's comparison, not
// for this getter.
func TestCSRFTokenFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := CSRFTokenFromRequest(req); got != "" {
		t.Errorf("no cookie: CSRFTokenFromRequest = %q, want empty", got)
	}
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok123"})
	if got := CSRFTokenFromRequest(req); got != "tok123" {
		t.Errorf("with cookie: CSRFTokenFromRequest = %q, want %q", got, "tok123")
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

// The CSRF cookie must be SameSite=Strict (no cross-site round-trip on
// state-changing requests) and Secure whenever the request is over TLS; over
// plain HTTP it must stay non-Secure or an http deployment drops its own cookie.
func TestCSRFCookieAttributes(t *testing.T) {
	issue := func(req *http.Request) *http.Cookie {
		rec := httptest.NewRecorder()
		if _, err := CSRFToken(rec, req); err != nil {
			t.Fatalf("CSRFToken: %v", err)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("no cookie set")
		}
		return cookies[0]
	}
	c := issue(httptest.NewRequest(http.MethodGet, "/", nil))
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Secure {
		t.Error("Secure = true over plain HTTP, want false")
	}
	tlsReq := httptest.NewRequest(http.MethodGet, "/", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	if c := issue(tlsReq); !c.Secure {
		t.Error("Secure = false over TLS, want true")
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

// TestClientIPPrefersCDNHeaders checks that when the socket peer is a trusted
// proxy (i.e. a real CDN is in front), the single-value client-IP header set by
// that CDN wins over the X-Forwarded-For chain. Without this, a Cloudflare
// deployment attributes every request to the same Cloudflare edge IP.
func TestClientIPPrefersCDNHeaders(t *testing.T) {
	tp, err := ParseTrustedProxies("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, header, value, xff, want string
	}{
		{"CF-Connecting-IP", "CF-Connecting-IP", "198.51.100.7", "9.9.9.9", "198.51.100.7"},
		{"True-Client-IP", "True-Client-IP", "203.0.113.4", "9.9.9.9", "203.0.113.4"},
		{"X-Real-IP", "X-Real-IP", "192.0.2.55", "9.9.9.9", "192.0.2.55"},
		{"header with trailing list", "CF-Connecting-IP", "198.51.100.7, 10.0.0.2", "9.9.9.9", "198.51.100.7"},
		{"garbage CDN header falls back to XFF", "CF-Connecting-IP", "not-an-ip", "203.0.113.4", "203.0.113.4"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:9000"
		req.Header.Set(c.header, c.value)
		req.Header.Set("X-Forwarded-For", c.xff)
		if got := tp.ClientIP(req); got != c.want {
			t.Errorf("%s: ClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// A direct (untrusted) client must not be able to spoof the CDN headers.
func TestClientIPIgnoresCDNHeadersFromUntrustedPeer(t *testing.T) {
	tp, err := ParseTrustedProxies("")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	req.Header.Set("X-Real-IP", "5.6.7.8")
	if got := tp.ClientIP(req); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the socket peer 203.0.113.9", got)
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
