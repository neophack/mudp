package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mudp/internal/config"
	"mudp/internal/portfwd"
	"mudp/internal/store"
)

// bootGateTestServer brings up a full App (store + router) with the forward
// login-gate enabled, plus one admin and one ordinary user session.
func bootGateTestServer(t *testing.T) (a *App, adminCookie, userCookie, otherName string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gatetest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	const adminPass = "GateTest-Admin-Pass-2026!"
	if err := db.Migrate("gateadmin", adminPass); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.SaveForwardAuthConfig(store.ForwardAuthConfig{Enabled: true}); err != nil {
		t.Fatalf("enable forward auth: %v", err)
	}
	app, err := New(config.Config{
		DockerHost:         "tcp://127.0.0.1:1", // unreachable; the gate never talks to Docker
		SessionSecret:      "gate-test-session-secret",
		AdminUser:          "gateadmin",
		AdminPassword:      adminPass,
		CaptchaTestAnswers: true,
	}, db)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	login := func(username, password string) string {
		t.Helper()
		// The login endpoint needs a captcha; with CaptchaTestAnswers the
		// challenge answer rides in a response header, so do the two-step the
		// secClient in security_regression_test.go does. No cookie jar needed:
		// only the session cookie value matters to the gate.
		cid, answer := fetchGateCaptcha(t, ts.URL)
		body := `{"username":"` + username + `","password":"` + password + `","captchaId":"` + cid + `","captcha":"` + answer + `"}`
		resp, err := http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("login %s: %v", username, err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		for _, ck := range resp.Cookies() {
			if ck.Name == "mudp_session" {
				return ck.Value
			}
		}
		t.Fatalf("login %s: no session cookie", username)
		return ""
	}

	adminCookie = login("gateadmin", adminPass)

	groupID, err := db.DefaultUserGroupID()
	if err != nil {
		t.Fatalf("DefaultUserGroupID: %v", err)
	}
	const userPass = "GateTest-User-Pass-2026!"
	if err := db.CreateUser("gateuser", userPass, store.RoleUser, groupID, 5, 0); err != nil {
		t.Fatalf("create gateuser: %v", err)
	}
	userCookie = login("gateuser", userPass)
	return app, adminCookie, userCookie, "gateuser"
}

func fetchGateCaptcha(t *testing.T, baseURL string) (id, answer string) {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/captcha")
	if err != nil {
		t.Fatalf("captcha: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("X-Mudp-Captcha-Id"), resp.Header.Get("X-Mudp-Captcha-Answer")
}

// gateRequest drives one checkForwardAuth decision over a net.Pipe: it feeds
// a minimal GET with the given session cookie and returns the gate verdict
// plus whatever the gate wrote back to the client.
func gateRequest(t *testing.T, a *App, rule portfwd.Rule, cookie string) (allowed bool, status int) {
	t.Helper()
	client, srv := net.Pipe()
	t.Cleanup(func() { client.Close(); srv.Close() })
	// net.Pipe writes block until read. The gate treats `client` as the
	// browser side — it reads the request from it and writes refusals to it —
	// while this test speaks through `srv`. Drain `srv` concurrently so the
	// gate's writes never wedge on an unread pipe.
	respCh := make(chan []byte, 1)
	go func() {
		_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
		var all []byte
		buf := make([]byte, 1024)
		for {
			n, err := srv.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil || n == 0 {
				break
			}
		}
		respCh <- all
	}()
	go func() {
		req := "GET / HTTP/1.1\r\nHost: x:10001\r\nCookie: mudp_session=" + cookie + "\r\n\r\n"
		_, _ = srv.Write([]byte(req))
	}()
	_, ok := a.checkForwardAuth(rule, client)
	if ok {
		return true, 0
	}
	// Pull the status line out of whatever the gate wrote.
	line := string(<-respCh)
	if !strings.HasPrefix(line, "HTTP/1.1 ") || len(line) < 12 {
		return false, -1
	}
	var code int
	if _, err := fmt.Sscanf(line[9:12], "%d", &code); err != nil {
		return false, -1
	}
	return false, code
}

// TestForwardGateRequiresRuleOwner pins the isolation contract of the login
// gate: a gated forwarded port belongs to the user whose container published
// it, so another authenticated account must not pass. Admins and unattributed
// (admin-curated manual) rules stay open to every signed-in user.
func TestForwardGateRequiresRuleOwner(t *testing.T) {
	a, adminCookie, userCookie, _ := bootGateTestServer(t)

	ownerRule := portfwd.Rule{HostPort: 10001, Proto: "tcp", Owner: "gateuser", RequireLogin: true}
	foreignRule := portfwd.Rule{HostPort: 10002, Proto: "tcp", Owner: "someoneelse", RequireLogin: true}
	manualRule := portfwd.Rule{HostPort: 10003, Proto: "tcp", Owner: "", RequireLogin: true, Source: "manual"}

	// The owner passes their own gate.
	if ok, _ := gateRequest(t, a, ownerRule, userCookie); !ok {
		t.Error("owner should pass their own gated forward")
	}
	// Another signed-in user is refused on someone else's port.
	if ok, status := gateRequest(t, a, foreignRule, userCookie); ok || status != http.StatusForbidden {
		t.Errorf("foreign user on another user's forward: allowed=%v status=%d, want 403", ok, status)
	}
	// Admins pass every gate.
	if ok, _ := gateRequest(t, a, foreignRule, adminCookie); !ok {
		t.Error("admin should pass any gated forward")
	}
	// An unattributed manual rule stays open to every signed-in user.
	if ok, _ := gateRequest(t, a, manualRule, userCookie); !ok {
		t.Error("unattributed manual forward should pass any signed-in user")
	}
}
