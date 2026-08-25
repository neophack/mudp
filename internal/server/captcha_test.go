package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mudp/internal/config"
	"mudp/internal/store"
)

func newCaptchaTestApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/captcha.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	const pass = "Captcha-Test-Pass-2026!"
	if err := db.Migrate("capadmin", pass); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	app, err := New(config.Config{
		DockerHost:         "tcp://127.0.0.1:1",
		SessionSecret:      "captcha-test-session-secret",
		AdminUser:          "capadmin",
		AdminPassword:      pass,
		CaptchaTestAnswers: true,
	}, db)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func fetchCaptcha(t *testing.T, h http.Handler) (id, answer string, body []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/captcha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("captcha status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/gif" {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "GIF8") {
		t.Fatal("body is not a GIF")
	}
	return rec.Header().Get("X-Mudp-Captcha-Id"), rec.Header().Get("X-Mudp-Captcha-Answer"), rec.Body.Bytes()
}

func TestCaptchaStoreVerifyIsSingleUse(t *testing.T) {
	s := newCaptchaStore()
	s.set("id1", "ABCDE")
	if !s.verify("id1", "abcde") {
		t.Fatal("case-insensitive match should pass")
	}
	// The correct answer again must fail: consumed by the first verify.
	if s.verify("id1", "ABCDE") {
		t.Fatal("captcha should be single-use")
	}
	s.set("id2", "XYZ23")
	if s.verify("id2", "WRONG") {
		t.Fatal("wrong code should fail")
	}
	if s.verify("id2", "XYZ23") {
		t.Fatal("failed attempt should consume the captcha too")
	}
}

func TestCaptchaStoreExpiry(t *testing.T) {
	s := newCaptchaStore()
	s.entries["id3"] = captchaEntry{answer: "ABCDE", expiry: time.Now().Add(-time.Second)}
	if s.verify("id3", "ABCDE") {
		t.Fatal("expired captcha should fail")
	}
}

func TestCaptchaEndpointAndLogin(t *testing.T) {
	app := newCaptchaTestApp(t)
	h := app.Routes()

	id, answer, _ := fetchCaptcha(t, h)
	if len(answer) != captchaLength {
		t.Fatalf("answer length = %d", len(answer))
	}

	post := func(payload string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec
	}

	// Missing captcha → rejected before the password is even checked.
	if rec := post(`{"username":"capadmin","password":"Captcha-Test-Pass-2026!"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login without captcha = %d", rec.Code)
	}
	// Wrong captcha.
	if rec := post(`{"username":"capadmin","password":"Captcha-Test-Pass-2026!","captchaId":"` + id + `","captcha":"ZZZZZ"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with wrong captcha = %d", rec.Code)
	}
	// Right captcha but the challenge was consumed by the wrong attempt above.
	if rec := post(`{"username":"capadmin","password":"Captcha-Test-Pass-2026!","captchaId":"` + id + `","captcha":"` + answer + `"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed captcha should be rejected, got %d", rec.Code)
	}
	// Fresh captcha + correct credentials → success.
	id2, answer2, _ := fetchCaptcha(t, h)
	rec := post(`{"username":"capadmin","password":"Captcha-Test-Pass-2026!","captchaId":"` + id2 + `","captcha":"` + answer2 + `"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login with valid captcha = %d: %s", rec.Code, rec.Body.String())
	}
	// Valid captcha does not rescue a wrong password.
	id3, answer3, _ := fetchCaptcha(t, h)
	rec = post(`{"username":"capadmin","password":"nope","captchaId":"` + id3 + `","captcha":"` + answer3 + `"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with wrong password = %d", rec.Code)
	}
}

// Without the test hook the answer must never leave the server.
func TestCaptchaAnswerHeaderHiddenByDefault(t *testing.T) {
	app := newCaptchaTestApp(t)
	app.cfg.CaptchaTestAnswers = false
	h := app.Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/captcha", nil))
	if rec.Header().Get("X-Mudp-Captcha-Answer") != "" {
		t.Fatal("answer header must be empty unless CaptchaTestAnswers is enabled")
	}
}
