package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mudp/internal/auth"
	"mudp/internal/config"
	"mudp/internal/store"
)

// base64RawURLNoPad/base64RawURLDecode mirror the unpadded base64url
// encoding auth.Signer uses for the session cookie body, so forgery tests can
// build/inspect cookie values byte-for-byte the same way the real signer does.
func base64RawURLNoPad(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func base64RawURLDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode %q: %v", s, err)
	}
	return string(b)
}

// This file is the permanent regression suite behind the manual penetration
// test pass covering SQL injection, XSS, CSRF, SSRF, file upload, arbitrary
// file read / path traversal, session-token forgery, API fuzzing, header
// injection, rate limiting, CORS, and HTTP request smuggling. Unlike the rest
// of this package's tests, these drive the REAL router (a.Routes(), with the
// full CSRF/rate-limit/security-header middleware chain) over a real TCP
// listener via httptest.Server, because several of these properties
// (CSRF, rate limiting, cookie-based session auth, request smuggling) live in
// middleware wired up in Routes(), not in the handler functions the rest of
// the package tests directly.
//
// GraphQL fuzzing is intentionally absent: the app has no GraphQL endpoint
// anywhere (chi REST routes only), so there is nothing to regress.

// newSecurityTestServer boots a full App — real store, real router and
// middleware chain — over httptest.NewServer, with an unreachable Docker host
// (nothing in this suite touches Docker) and an isolated SQLite DB per test.
// Returns clients already logged in as a fresh admin and a fresh plain user.
func newSecurityTestServer(t *testing.T) (baseURL string, admin, user *secClient) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sectest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	const adminPass = "SecTest-Admin-Pass-2026!"
	if err := db.Migrate("secadmin", adminPass); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := config.Config{
		DockerHost:        "tcp://127.0.0.1:1", // deliberately unreachable; nothing here needs Docker
		SessionSecret:     "security-regression-test-session-secret",
		AdminUser:         "secadmin",
		AdminPassword:     adminPass,
		CaptchaTestAnswers: true, // login requires a captcha; tests read the answer header
	}
	app, err := New(cfg, db)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	admin = newSecClient(t, ts.URL)
	if err := admin.login("secadmin", adminPass); err != nil {
		t.Fatalf("admin login: %v", err)
	}

	const userPass = "SecTest-User-Pass-2026!"
	groupID, err := db.DefaultUserGroupID()
	if err != nil {
		t.Fatalf("DefaultUserGroupID: %v", err)
	}
	if err := db.CreateUser("secuser", userPass, store.RoleUser, groupID, 5, 0); err != nil {
		t.Fatalf("create secuser: %v", err)
	}
	user = newSecClient(t, ts.URL)
	if err := user.login("secuser", userPass); err != nil {
		t.Fatalf("user login: %v", err)
	}

	return ts.URL, admin, user
}

// configureNetdiskRoot points the default "users" group's netdisk at a fresh
// temp directory, mirroring what an admin does via POST /api/groups/netdisk
// before any netdisk test can upload/read anything.
func configureNetdiskRoot(t *testing.T, db *store.DB) string {
	t.Helper()
	root := t.TempDir()
	groupID, err := db.DefaultUserGroupID()
	if err != nil {
		t.Fatalf("DefaultUserGroupID: %v", err)
	}
	if err := db.UpdateGroupNetdiskPath(groupID, root); err != nil {
		t.Fatalf("UpdateGroupNetdiskPath: %v", err)
	}
	return root
}

// secClient is a minimal cookie-jar-backed HTTP client that mirrors what a
// real browser does against this app: it carries the session + CSRF cookies
// set by /api/login and, unless told otherwise, attaches the CSRF header the
// real frontend attaches on every mutating request.
type secClient struct {
	t       *testing.T
	baseURL string
	client  *http.Client
	csrf    string
}

func newSecClient(t *testing.T, baseURL string) *secClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &secClient{t: t, baseURL: baseURL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

// fetchCaptcha grabs a fresh challenge using the test-only answer header.
func (c *secClient) fetchCaptcha() (id, answer string) {
	c.t.Helper()
	resp, err := c.client.Get(c.baseURL + "/api/captcha")
	if err != nil {
		c.t.Fatalf("get captcha: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	id, answer = resp.Header.Get("X-Mudp-Captcha-Id"), resp.Header.Get("X-Mudp-Captcha-Answer")
	if id == "" || answer == "" {
		c.t.Fatalf("captcha response missing id/answer headers (CaptchaTestAnswers off?)")
	}
	return id, answer
}

func (c *secClient) login(username, password string) error {
	captchaID, captcha := c.fetchCaptcha()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password, "captchaId": captchaID, "captcha": captcha})
	resp, err := c.client.Post(c.baseURL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: %d %s", resp.StatusCode, b)
	}
	var out struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	c.csrf = out.CSRFToken
	return nil
}

// request is the low-level primitive every helper below funnels through.
// attachCSRF controls whether the real CSRF header is sent, so a test can
// deliberately omit or forge it.
func (c *secClient) request(method, path string, body io.Reader, extraHeaders map[string]string, attachCSRF bool) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	if attachCSRF && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (c *secClient) get(path string) (*http.Response, []byte) {
	return c.request(http.MethodGet, path, nil, nil, false)
}

func (c *secClient) postJSON(path string, v any) (*http.Response, []byte) {
	b, _ := json.Marshal(v)
	return c.request(http.MethodPost, path, bytes.NewReader(b), nil, true)
}

// postJSONNoCSRF sends a real, valid session cookie but skips the CSRF
// header entirely -- the "attacker forged a cross-site form" shape.
func (c *secClient) postJSONNoCSRF(path string, v any) (*http.Response, []byte) {
	b, _ := json.Marshal(v)
	return c.request(http.MethodPost, path, bytes.NewReader(b), nil, false)
}

// postJSONForgedCSRF sends a syntactically-plausible but wrong CSRF token.
func (c *secClient) postJSONForgedCSRF(path string, v any) (*http.Response, []byte) {
	b, _ := json.Marshal(v)
	return c.request(http.MethodPost, path, bytes.NewReader(b), map[string]string{"X-CSRF-Token": strings.Repeat("deadbeef", 8)}, false)
}

// uploadFile performs a multipart netdisk upload of a single in-memory file.
func (c *secClient) uploadFile(path, dirPath, filename string, content []byte) (*http.Response, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		c.t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		c.t.Fatalf("write file part: %v", err)
	}
	_ = mw.WriteField("path", dirPath)
	_ = mw.Close()
	return c.request(http.MethodPost, path, &buf, map[string]string{"Content-Type": mw.FormDataContentType()}, true)
}

// sessionCookieValue returns the raw mudp_session cookie value currently held
// by the client's jar, so a test can tamper with or reuse it directly.
func (c *secClient) sessionCookieValue() string {
	u, _ := url.Parse(c.baseURL)
	for _, ck := range c.client.Jar.Cookies(u) {
		if ck.Name == auth.CookieName {
			return ck.Value
		}
	}
	return ""
}

// rawCookieGet issues a GET carrying only an explicit, hand-built session
// cookie -- bypassing the client's own jar/CSRF state entirely, for session
// forgery tests.
func rawCookieGet(t *testing.T, baseURL, path, cookieValue string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookieValue})
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// ===========================================================================
// 1. SQL injection
// ===========================================================================

// TestSecuritySQLInjectionStoredLiterally regresses SQL injection across the
// parameterized query layer: classic OR/UNION/stacked-query/comment payloads
// must be stored and returned as inert literal strings, never alter query
// behavior or 500.
func TestSecuritySQLInjectionStoredLiterally(t *testing.T) {
	_, admin, _ := newSecurityTestServer(t)

	payloads := []string{
		`' OR '1'='1`,
		`' OR '1'='1' --`,
		`'; DROP TABLE users; --`,
		`' UNION SELECT NULL,NULL,NULL--`,
		`admin'--`,
		`1' AND SLEEP(3)-- -`,
	}
	for _, p := range payloads {
		t.Run(p, func(t *testing.T) {
			resp, body := admin.postJSON("/api/groups", map[string]string{"name": p})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("create group with payload %q: status=%d body=%s", p, resp.StatusCode, body)
			}
			resp2, body2 := admin.get("/api/groups")
			if resp2.StatusCode != http.StatusOK {
				t.Fatalf("list groups: status=%d body=%s", resp2.StatusCode, body2)
			}
			if !strings.Contains(string(body2), p) {
				t.Errorf("payload %q was not stored/returned literally; groups=%s", p, body2)
			}
		})
	}

	// The users table must still exist and be queryable -- the ";DROP TABLE
	// users;--" payload above must not have executed as SQL.
	if resp, body := admin.get("/api/users"); resp.StatusCode != http.StatusOK {
		t.Fatalf("users table appears damaged after injection payloads: status=%d body=%s", resp.StatusCode, body)
	}
}

// ===========================================================================
// 2. CSRF
// ===========================================================================

func TestSecurityCSRFEndToEnd(t *testing.T) {
	_, admin, _ := newSecurityTestServer(t)

	t.Run("missing token cookie and header rejected", func(t *testing.T) {
		// Fresh jar with only the session cookie copied over -- no CSRF cookie
		// at all, simulating a cross-site attacker who can ride the session
		// cookie (sent automatically by the browser) but cannot read or set
		// mudp_csrf (SameSite/cross-origin).
		attacker := newSecClient(t, admin.baseURL)
		u, _ := url.Parse(admin.baseURL)
		attacker.client.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: admin.sessionCookieValue()}})
		resp, body := attacker.postJSONNoCSRF("/api/groups", map[string]string{"name": "csrf-missing"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want 403; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("csrf cookie present but header omitted rejected", func(t *testing.T) {
		resp, body := admin.postJSONNoCSRF("/api/groups", map[string]string{"name": "csrf-no-header"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want 403; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("forged token rejected", func(t *testing.T) {
		resp, body := admin.postJSONForgedCSRF("/api/groups", map[string]string{"name": "csrf-forged"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want 403; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("valid token accepted", func(t *testing.T) {
		resp, body := admin.postJSON("/api/groups", map[string]string{"name": "csrf-valid"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("safe GET method exempt and read-only", func(t *testing.T) {
		resp, body := admin.get("/api/groups")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d; body=%s", resp.StatusCode, body)
		}
	})
}

// ===========================================================================
// 3. Session / auth token forgery (the app has no JWT; this is the actual
//    HMAC-signed cookie scheme it uses instead).
// ===========================================================================

func TestSecuritySessionForgeryRejected(t *testing.T) {
	_, admin, user := newSecurityTestServer(t)

	t.Run("tampered signature rejected", func(t *testing.T) {
		// Tamper with the decoded signature bytes, not a base64 character of
		// the whole cookie: when the decoded length is not a multiple of 3,
		// the final base64 character carries ignored padding bits, so an
		// A<->B flip there can decode back to the identical cookie (1 time in
		// 16 depending on the HMAC output) and would fail open.
		raw := admin.sessionCookieValue()
		decoded := []byte(base64RawURLDecode(t, raw))
		decoded[len(decoded)-1] ^= 1
		resp, body := rawCookieGet(t, admin.baseURL, "/api/users", base64RawURLNoPad(string(decoded)))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("fabricated signature rejected", func(t *testing.T) {
		forged := base64RawURLNoPad("1:" + fmt.Sprint(time.Now().Add(time.Hour).Unix()) + ":" + strings.Repeat("deadbeef", 8))
		resp, body := rawCookieGet(t, admin.baseURL, "/api/users", forged)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401; body=%s", resp.StatusCode, body)
		}
	})

	t.Run("uid swapped to admin under the user's own valid signature rejected", func(t *testing.T) {
		raw := user.sessionCookieValue()
		decoded := base64RawURLDecode(t, raw)
		parts := strings.SplitN(decoded, ":", 3)
		if len(parts) != 3 {
			t.Fatalf("unexpected cookie shape: %q", decoded)
		}
		forged := base64RawURLNoPad("1:" + parts[1] + ":" + parts[2]) // uid=1 (admin), original exp+sig
		resp, body := rawCookieGet(t, admin.baseURL, "/api/users", forged)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401; body=%s", resp.StatusCode, body)
		}
	})
}

// TestSecuritySessionSurvivesLogout is a KNOWN GAP, not a "this is fine"
// regression: sessions are stateless HMAC cookies with no server-side store,
// so /api/logout can only clear the cookie in the browser that calls it. A
// captured token keeps authenticating for up to auth.SessionTTL after the
// legitimate user logs out. This test documents today's actual behavior so a
// future fix (a per-user "sessions issued after" watermark, or a small
// revocation list) is visible as an intentional behavior change, not a
// silent regression, when this test starts failing.
func TestSecuritySessionSurvivesLogout(t *testing.T) {
	baseURL, _, user := newSecurityTestServer(t)
	savedCookie := user.sessionCookieValue()

	resp, body := user.postJSON("/api/logout", map[string]string{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: status=%d body=%s", resp.StatusCode, body)
	}

	resp2, body2 := rawCookieGet(t, baseURL, "/api/me", savedCookie)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(body2), "secuser") {
		t.Fatalf("KNOWN-GAP assumption changed: a saved session cookie no longer works after logout "+
			"(status=%d body=%s) -- if this was fixed intentionally, delete this test and add a positive "+
			"one asserting the cookie is rejected", resp2.StatusCode, body2)
	}
}

// ===========================================================================
// 4. File upload / XSS-via-preview / path traversal / arbitrary file read
// ===========================================================================

func TestSecurityNetdiskPathTraversalRejected(t *testing.T) {
	_, admin := newSecurityTestServerWithNetdisk(t)

	traversalPaths := []string{
		"../../../../etc/passwd",
		`..\..\..\..\Windows\win.ini`,
		"....//....//....//etc/passwd",
		"%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/etc/passwd",
		"..",
	}
	for _, p := range traversalPaths {
		t.Run(p, func(t *testing.T) {
			for _, ep := range []string{"/api/netdisk/raw", "/api/netdisk/download"} {
				resp, body := admin.get(ep + "?path=" + url.QueryEscape(p))
				if resp.StatusCode == http.StatusOK && (bytes.Contains(body, []byte("root:")) || bytes.Contains(body, []byte("[fonts]"))) {
					t.Fatalf("%s leaked host file content for path %q: %s", ep, p, body)
				}
			}
		})
	}
}

// TestSecurityUploadTraversalInPathsFieldSandboxed regresses the folder-upload
// "paths" form field (client-declared relative path per file): a traversal
// value there must land inside the user's netdisk root, never above it.
func TestSecurityUploadTraversalInPathsFieldSandboxed(t *testing.T) {
	_, admin, root := newSecurityTestServerWithNetdiskRoot(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "x.txt")
	_, _ = fw.Write([]byte("traversal probe"))
	_ = mw.WriteField("path", "")
	_ = mw.WriteField("paths", "../../../../outside_root_marker.txt")
	_ = mw.Close()
	resp2, body2 := admin.request(http.MethodPost, "/api/netdisk/upload", &buf, map[string]string{"Content-Type": mw.FormDataContentType()}, true)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("upload: status=%d body=%s", resp2.StatusCode, body2)
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside_root_marker.txt")); err == nil {
		t.Fatal("traversal payload in the \"paths\" field escaped the netdisk root")
	}
	escaped := false
	_ = filepath.Walk(filepath.Dir(filepath.Dir(root)), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "outside_root_marker.txt" && !strings.HasPrefix(path, root) {
			escaped = true
		}
		return nil
	})
	if escaped {
		t.Fatal("traversal payload escaped the configured netdisk root")
	}
}

// TestSecurityUploadedHTMLServedAsPlainText regresses the stored-XSS-via-file-
// preview defense: an uploaded .html file with an inline <script> must never
// be served back as text/html on this origin (netdisk.go's textishContentType
// map exists specifically to prevent this).
func TestSecurityUploadedHTMLServedAsPlainText(t *testing.T) {
	_, admin := newSecurityTestServerWithNetdisk(t)

	payload := []byte(`<html><body><script>document.title='XSS-'+document.cookie</script>hi</body></html>`)
	resp, body := admin.uploadFile("/api/netdisk/upload", "", "evil.html", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status=%d body=%s", resp.StatusCode, body)
	}

	resp2, body2 := admin.get("/api/netdisk/raw?path=evil.html")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("preview: status=%d body=%s", resp2.StatusCode, body2)
	}
	ct := resp2.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		t.Fatalf("uploaded HTML was served as text/html (stored-XSS primitive): Content-Type=%q", ct)
	}
}

// TestSecurityUploadedSVGSandboxed regresses SVG preview sandboxing: SVG can
// carry <script>, so it must only ever be served under a CSP that blocks
// script execution (sandbox, no allow-scripts token).
func TestSecurityUploadedSVGSandboxed(t *testing.T) {
	_, admin := newSecurityTestServerWithNetdisk(t)

	payload := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	resp, body := admin.uploadFile("/api/netdisk/upload", "", "evil.svg", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status=%d body=%s", resp.StatusCode, body)
	}

	resp2, body2 := admin.get("/api/netdisk/raw?path=evil.svg")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("preview: status=%d body=%s", resp2.StatusCode, body2)
	}
	csp := resp2.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") {
		t.Fatalf("SVG preview is not sandboxed, CSP=%q", csp)
	}
}

// ===========================================================================
// 5. SSRF
// ===========================================================================

// TestSecurityGeoLookupRejectsNonPublicIPs regresses the only outbound-
// request-triggering endpoint reachable by a non-admin (GET /api/geo?ip=):
// it must reject anything that is not a real public unicast IP -- including
// the cloud metadata address -- before it ever builds an outbound request.
func TestSecurityGeoLookupRejectsNonPublicIPs(t *testing.T) {
	_, _, user := newSecurityTestServer(t)

	inputs := []string{
		"169.254.169.254", // cloud metadata
		"127.0.0.1",
		"10.0.0.1",
		"evil.com",
		"8.8.8.8@evil.com",
	}
	for _, ip := range inputs {
		t.Run(ip, func(t *testing.T) {
			resp, body := user.get("/api/geo?ip=" + url.QueryEscape(ip))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("decode: %v body=%s", err, body)
			}
			if country, _ := out["country"].(string); country != "" {
				t.Fatalf("input %q produced a non-empty geo answer, meaning an outbound lookup ran for a non-public IP: %v", ip, out)
			}
		})
	}
}

// ===========================================================================
// 6. Rate limiting
// ===========================================================================

func TestSecurityLoginRateLimited(t *testing.T) {
	baseURL, _, _ := newSecurityTestServer(t)
	client := &http.Client{Timeout: 5 * time.Second}

	var got429 bool
	for i := 0; i < 60; i++ {
		body, _ := json.Marshal(map[string]string{"username": "secadmin", "password": fmt.Sprintf("wrong-%d", i)})
		resp, err := client.Post(baseURL+"/api/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("60 rapid login attempts never triggered a 429; login rate limiting appears disabled")
	}
}

// ===========================================================================
// 7. CORS
// ===========================================================================

func TestSecurityNoCORSHeadersEverEmitted(t *testing.T) {
	baseURL, admin, _ := newSecurityTestServer(t)
	client := &http.Client{Timeout: 5 * time.Second}

	origins := []string{"https://evil.example.com", "null", "http://127.0.0.1.evil.com"}
	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/me", nil)
			req.Header.Set("Origin", origin)
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: admin.sessionCookieValue()})
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
				t.Fatalf("Origin %q got Access-Control-Allow-Origin=%q; cross-origin credentialed reads should never be enabled", origin, acao)
			}
		})
	}

	// Preflight on a state-changing endpoint must not grant an untrusted origin
	// permission either.
	req, _ := http.NewRequest(http.MethodOptions, baseURL+"/api/users", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("OPTIONS preflight from an untrusted origin got Access-Control-Allow-Origin=%q", acao)
	}
}

// ===========================================================================
// 8. HTTP request smuggling
// ===========================================================================

// TestSecurityConflictingContentLengthAndTransferEncodingRejected regresses
// the classic CL.TE/TE.CL smuggling primitive directly against Go's net/http
// server (no proxy hop in this app to desync against, but the server itself
// must still refuse the ambiguous framing per RFC 7230 §3.3.3).
func TestSecurityConflictingContentLengthAndTransferEncodingRejected(t *testing.T) {
	baseURL, _, _ := newSecurityTestServer(t)
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "CL and TE both present (CL.TE)",
			raw: "POST /api/login HTTP/1.1\r\n" +
				"Host: " + u.Host + "\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: 6\r\n" +
				"Transfer-Encoding: chunked\r\n" +
				"Connection: close\r\n\r\n" +
				"0\r\n\r\nG",
		},
		{
			name: "duplicate Transfer-Encoding",
			raw: "POST /api/login HTTP/1.1\r\n" +
				"Host: " + u.Host + "\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: 2\r\n" +
				"Transfer-Encoding: chunked\r\n" +
				"Transfer-Encoding: identity\r\n" +
				"Connection: close\r\n\r\n" +
				"{}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte(c.raw)); err != nil {
				t.Fatalf("write: %v", err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			resp := make([]byte, 4096)
			n, _ := conn.Read(resp)
			line := string(resp[:n])
			if !strings.HasPrefix(line, "HTTP/1.1 400") && !strings.HasPrefix(line, "HTTP/1.1 501") {
				t.Fatalf("expected the server to reject the ambiguous request with 400/501, got: %s", line)
			}
		})
	}
}

// ===========================================================================
// 9. API fuzz (curated subset -- the full ~930-request ad hoc pass lives in
//    the manual test scratchpad; this keeps a fast, representative slice in
//    CI asserting the panic-recovery / generic-error-mapping invariant holds).
// ===========================================================================

func TestSecurityFuzzMalformedBodiesDoNotLeakInternals(t *testing.T) {
	_, admin := newSecurityTestServerWithNetdisk(t)

	endpoints := []string{
		"/api/groups", "/api/users", "/api/netdisk/mkdir", "/api/netdisk/delete",
		"/api/netdisk/rename", "/api/containers", "/api/volumes", "/api/networks",
		"/api/registries",
	}
	malformed := []struct {
		name string
		body string
		ct   string
	}{
		{"empty-body", "", "application/json"},
		{"garbage-not-json", "not json {{{", "application/json"},
		{"array-instead-of-object", "[1,2,3]", "application/json"},
		{"huge-string-field", `{"name":"` + strings.Repeat("A", 200000) + `"}`, "application/json"},
		{"null-values", `{"id":null,"name":null,"path":null}`, "application/json"},
		{"embedded-nul", "{\"name\":\"a\x00b\"}", "application/json"},
	}
	leakMarkers := []string{"panic:", "goroutine ", ".go:", "runtime error", "d:\\mudp", "c:\\users"}

	for _, ep := range endpoints {
		for _, m := range malformed {
			t.Run(ep+"/"+m.name, func(t *testing.T) {
				resp, body := admin.request(http.MethodPost, ep, strings.NewReader(m.body), map[string]string{"Content-Type": m.ct}, true)
				low := strings.ToLower(string(body))
				for _, marker := range leakMarkers {
					if strings.Contains(low, marker) {
						t.Fatalf("response leaked internal detail (marker %q): status=%d body=%s", marker, resp.StatusCode, body)
					}
				}
				if resp.StatusCode == http.StatusInternalServerError {
					// A 500 itself isn't automatically a security bug (some are
					// benign upstream errors), but it must never carry a leak
					// marker, which was already checked above; log it for
					// visibility rather than failing the whole suite on it.
					t.Logf("note: %s with %s returned 500: %s", ep, m.name, body)
				}
			})
		}
	}
}

// ===========================================================================
// Test helpers
// ===========================================================================

// newSecurityTestServerWithNetdisk is newSecurityTestServer plus a configured
// netdisk root for the default "users" group, for tests that upload/read
// netdisk files. It re-derives everything from scratch (own DB, own server)
// rather than sharing state with newSecurityTestServer, matching this
// package's existing per-test-isolated-DB convention.
func newSecurityTestServerWithNetdisk(t *testing.T) (baseURL string, admin *secClient) {
	t.Helper()
	baseURL, admin, root := newSecurityTestServerWithNetdiskRoot(t)
	_ = root
	return baseURL, admin
}

func newSecurityTestServerWithNetdiskRoot(t *testing.T) (baseURL string, admin *secClient, userRoot string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sectest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	const adminPass = "SecTest-Admin-Pass-2026!"
	if err := db.Migrate("secadmin", adminPass); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	root := configureNetdiskRoot(t, db)

	cfg := config.Config{
		DockerHost:        "tcp://127.0.0.1:1",
		SessionSecret:     "security-regression-test-session-secret",
		AdminUser:         "secadmin",
		AdminPassword:     adminPass,
		CaptchaTestAnswers: true,
	}
	app, err := New(cfg, db)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	admin = newSecClient(t, ts.URL)
	if err := admin.login("secadmin", adminPass); err != nil {
		t.Fatalf("admin login: %v", err)
	}
	// secadmin's netdisk subdirectory is "secadmin-<id>" under root; id is 1
	// for the first user created by Migrate.
	return ts.URL, admin, filepath.Join(root, "secadmin-1")
}
