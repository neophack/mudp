package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mintCookie replicates the signing scheme from auth.go to hand-craft cookie
// values for tests: the value is base64url("uid:exp:sig") where sig is
// base64url(HMAC-SHA256(secret, "uid:exp")). Tests must not rely on the
// unexported Signer.sign so a broken signer cannot make UserID agree with
// itself.
func mintCookie(secret string, uid, exp int64) string {
	body := fmt.Sprintf("%d:%d", uid, exp)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(body + ":" + sig))
}

// requestWithCookie returns a request carrying the given session cookie.
func requestWithCookie(value string) *http.Request {
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: value})
	return r
}

// TestSignerSetUserIDRoundTrip issues a cookie through a recorder, attaches it
// to a fresh request the way a browser would, and expects the same user ID.
func TestSignerSetUserIDRoundTrip(t *testing.T) {
	s := New("test-secret")
	for _, uid := range []int64{1, 42, 1 << 40} {
		rec := httptest.NewRecorder()
		s.Set(rec, httptest.NewRequest("GET", "https://x/login", nil), uid)

		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("uid %d: got %d cookies, want 1", uid, len(cookies))
		}
		if cookies[0].Name != CookieName {
			t.Fatalf("uid %d: cookie name = %q, want %q", uid, cookies[0].Name, CookieName)
		}

		got, ok := s.UserID(requestWithCookie(cookies[0].Value))
		if !ok {
			t.Fatalf("uid %d: UserID rejected the cookie it just issued", uid)
		}
		if got != uid {
			t.Errorf("UserID = %d, want %d", got, uid)
		}
	}
}

// TestSignerTamperedSignature flips one character in the signature segment of
// an otherwise valid cookie; verification must fail.
func TestSignerTamperedSignature(t *testing.T) {
	s := New("test-secret")
	rec := httptest.NewRecorder()
	s.Set(rec, httptest.NewRequest("GET", "http://x/login", nil), 7)
	issued := rec.Result().Cookies()[0].Value

	raw, err := base64.RawURLEncoding.DecodeString(issued)
	if err != nil {
		t.Fatalf("issued cookie is not base64: %v", err)
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 {
		t.Fatalf("issued cookie has %d parts, want 3", len(parts))
	}
	// Flip the first signature char to a different base64url char, keeping the
	// length and charset valid so the rejection comes from the HMAC check.
	sig := parts[2]
	replacement := byte('A')
	if sig[0] == 'A' {
		replacement = 'B'
	}
	parts[2] = string(replacement) + sig[1:]
	tampered := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, ":")))

	if uid, ok := s.UserID(requestWithCookie(tampered)); ok {
		t.Errorf("tampered signature accepted as user %d", uid)
	}
}

// TestSignerWrongSecret: a cookie issued under one secret must not verify
// under another.
func TestSignerWrongSecret(t *testing.T) {
	rec := httptest.NewRecorder()
	New("secret-a").Set(rec, httptest.NewRequest("GET", "http://x/login", nil), 9)
	value := rec.Result().Cookies()[0].Value

	if _, ok := New("secret-a").UserID(requestWithCookie(value)); !ok {
		t.Error("same-secret verification failed")
	}
	if uid, ok := New("secret-b").UserID(requestWithCookie(value)); ok {
		t.Errorf("cookie from secret-a verified under secret-b as user %d", uid)
	}
}

// TestSignerExpiredCookie crafts a correctly-signed cookie whose expiry is in
// the past; a valid signature must not rescue an expired session.
func TestSignerExpiredCookie(t *testing.T) {
	s := New("test-secret")
	value := mintCookie("test-secret", 5, time.Now().Add(-time.Hour).Unix())

	if uid, ok := s.UserID(requestWithCookie(value)); ok {
		t.Errorf("expired cookie accepted as user %d", uid)
	}
}

// TestSignerMalformedCookies feeds garbage values; all must be rejected
// without panicking.
func TestSignerMalformedCookies(t *testing.T) {
	s := New("test-secret")
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name  string
		value string
	}{
		{"empty value", ""},
		{"not base64", "!!!not-base64!!!"},
		{"base64 of garbage", b64("\x00\x01\x02\xff")},
		{"two parts", b64("1:9999999999")},
		{"four parts", b64("1:9999999999:abc:def")},
		{"bad signature", b64("1:9999999999:AAAA")},
		{"non-numeric expiry", b64("1:abc:x")},
		{"non-numeric uid with valid sig", func() string {
			// Hand-signed payload whose uid is not a number.
			body := "abc:9999999999"
			m := hmac.New(sha256.New, []byte("test-secret"))
			m.Write([]byte(body))
			return b64(body + ":" + base64.RawURLEncoding.EncodeToString(m.Sum(nil)))
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if uid, ok := s.UserID(requestWithCookie(c.value)); ok {
				t.Errorf("malformed cookie accepted as user %d", uid)
			}
		})
	}
}

// TestSignerNoCookie: a request without the session cookie is not authenticated.
func TestSignerNoCookie(t *testing.T) {
	if uid, ok := New("test-secret").UserID(httptest.NewRequest("GET", "http://x/", nil)); ok {
		t.Errorf("cookie-less request authenticated as user %d", uid)
	}
}

// TestSignerSetCookieAttributes pins the security attributes of the issued
// cookie: HttpOnly, SameSite=Lax, Path=/, ~24h expiry, and Secure only when
// the request is secure.
func TestSignerSetCookieAttributes(t *testing.T) {
	s := New("test-secret")
	cases := []struct {
		name       string
		request    *http.Request
		wantSecure bool
	}{
		{"tls request", httptest.NewRequest("GET", "https://x/login", nil), true},
		{"plain http", httptest.NewRequest("GET", "http://x/login", nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := time.Now()
			rec := httptest.NewRecorder()
			s.Set(rec, c.request, 1)
			after := time.Now()

			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies, want 1", len(cookies))
			}
			ck := cookies[0]
			if !ck.HttpOnly {
				t.Error("HttpOnly not set")
			}
			if ck.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", ck.SameSite)
			}
			if ck.Path != "/" {
				t.Errorf("Path = %q, want /", ck.Path)
			}
			if ck.Secure != c.wantSecure {
				t.Errorf("Secure = %v, want %v", ck.Secure, c.wantSecure)
			}
			// Set carries no Max-Age, only an Expires ~SessionTTL out.
			if ck.MaxAge != 0 {
				t.Errorf("MaxAge = %d, want 0 (unset)", ck.MaxAge)
			}
			if lo, hi := before.Add(SessionTTL-2*time.Second), after.Add(SessionTTL+2*time.Second); ck.Expires.Before(lo) || ck.Expires.After(hi) {
				t.Errorf("Expires = %v, want within [%v, %v]", ck.Expires, lo, hi)
			}
		})
	}
}

// TestSignerClear: the cleared cookie is empty and immediately expired.
func TestSignerClear(t *testing.T) {
	rec := httptest.NewRecorder()
	New("test-secret").Clear(rec, httptest.NewRequest("GET", "https://x/logout", nil))

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	ck := cookies[0]
	if ck.Name != CookieName {
		t.Errorf("cookie name = %q, want %q", ck.Name, CookieName)
	}
	if ck.Value != "" {
		t.Errorf("Value = %q, want empty", ck.Value)
	}
	if ck.Path != "/" {
		t.Errorf("Path = %q, want /", ck.Path)
	}
	if !ck.HttpOnly {
		t.Error("HttpOnly not set")
	}
	// MaxAge=-1 is serialized as "Max-Age=0" (delete now), which parses back
	// as 0; assert expiry against the raw header instead.
	raw := rec.Header().Get("Set-Cookie")
	if !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("Set-Cookie %q missing Max-Age=0 expiry", raw)
	}
}

// TestFeishuUserUsername pins the OpenID-to-username mapping: keep only ASCII
// letters and digits, fall back to "feishu-"+OpenID when nothing survives.
func TestFeishuUserUsername(t *testing.T) {
	cases := []struct {
		name   string
		openID string
		want   string
	}{
		{"normal openid", "ou_7d8a9e2f", "ou7d8a9e2f"},
		{"mixed case and digits kept", "AbC123", "AbC123"},
		{"underscores and dashes stripped", "on_abc-def_123", "onabcdef123"},
		{"unicode stripped", "用户_abc!!", "abc"},
		{"all symbols falls back", "!!!___", "feishu-!!!___"},
		{"unicode only falls back", "用户", "feishu-用户"},
		{"empty openid", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (FeishuUser{OpenID: c.openID}).Username(); got != c.want {
				t.Errorf("Username() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFeishuAuthorizeURL parses the built URL and checks the query params
// rather than string-matching the whole URL.
func TestFeishuAuthorizeURL(t *testing.T) {
	client := NewFeishuClient("cli_testappid", "ignored-secret")
	redirectURI := "https://app.example.com/auth/callback?next=/dash"
	state := "csrf-state-123"

	u, err := url.Parse(client.AuthorizeURL(redirectURI, state))
	if err != nil {
		t.Fatalf("AuthorizeURL returned unparseable URL: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("scheme = %q, want https", u.Scheme)
	}
	if u.Host != "open.feishu.cn" {
		t.Errorf("host = %q, want open.feishu.cn", u.Host)
	}
	if u.Path != "/open-apis/authen/v1/index" {
		t.Errorf("path = %q, want /open-apis/authen/v1/index", u.Path)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != "cli_testappid" {
		t.Errorf("client_id = %q, want cli_testappid", got)
	}
	if got := q.Get("redirect_uri"); got != redirectURI {
		t.Errorf("redirect_uri = %q, want %q", got, redirectURI)
	}
	if got := q.Get("state"); got != state {
		t.Errorf("state = %q, want %q", got, state)
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	// The redirect URI must appear escaped in the raw URL, not verbatim.
	if strings.Contains(u.String(), redirectURI) {
		t.Errorf("raw URL contains unescaped redirect_uri: %s", u)
	}
	if !strings.Contains(u.String(), url.QueryEscape(redirectURI)) {
		t.Errorf("raw URL missing escaped redirect_uri: %s", u)
	}
}
