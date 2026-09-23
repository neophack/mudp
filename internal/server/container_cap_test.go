package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mudp/internal/config"
	"mudp/internal/store"
)

// fakeDockerWithContainers serves just enough of the Engine API for the
// ownership and cap checks: the version ping and a container list holding
// `names`. Every other call is refused; mutating ones (create, start, ...) are
// also counted, so a test can assert the handler changed nothing in Docker.
// Reads are not counted: the background stats sampler polls on its own.
func fakeDockerWithContainers(t *testing.T, names []string) (host string, mutations *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.43")
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := make([]map[string]any, 0, len(names))
			for i, n := range names {
				list = append(list, map[string]any{
					"Id":     strings.Repeat(string(rune('a'+i)), 64),
					"Names":  []string{"/" + n},
					"Labels": map[string]string{"mudp.managed": "true"},
					"State":  "running",
				})
			}
			_ = json.NewEncoder(w).Encode(list)
		default:
			if r.Method != http.MethodGet {
				atomic.AddInt32(&calls, 1)
			}
			http.Error(w, `{"message":"not faked"}`, http.StatusNotImplemented)
		}
	}))
	t.Cleanup(ts.Close)
	return "tcp://" + strings.TrimPrefix(ts.URL, "http://"), &calls
}

// Duplicating a container creates one, so it is bound by the same container
// cap as the create wizard; it used to skip the check entirely, letting a user
// at their cap clone containers without limit.
func TestContainerDuplicateRespectsCap(t *testing.T) {
	dockerHost, mutations := fakeDockerWithContainers(t, []string{"mudp-capuser-web"})

	db, err := store.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("capadmin", "Cap-Admin-Pass-2026!"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	groupID, err := db.DefaultUserGroupID()
	if err != nil {
		t.Fatalf("DefaultUserGroupID: %v", err)
	}
	const userPass = "Cap-User-Pass-2026!"
	if err := db.CreateUser("capuser", userPass, store.RoleUser, groupID, 1, 0); err != nil {
		t.Fatalf("create user: %v", err)
	}
	app, err := New(config.Config{
		DockerHost:         dockerHost,
		SessionSecret:      "container-cap-test-session-secret",
		AdminUser:          "capadmin",
		AdminPassword:      "Cap-Admin-Pass-2026!",
		CaptchaTestAnswers: true,
	}, db)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	user := newSecClient(t, ts.URL)
	if err := user.login("capuser", userPass); err != nil {
		t.Fatalf("login: %v", err)
	}
	before := atomic.LoadInt32(mutations)
	resp, body := user.postJSON("/api/containers/duplicate", map[string]string{"id": strings.Repeat("a", 64), "name": "web2"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "container limit reached") {
		t.Fatalf("duplicate at cap = %d %s, want 400 container limit reached", resp.StatusCode, body)
	}
	if n := atomic.LoadInt32(mutations) - before; n != 0 {
		t.Fatalf("duplicate at cap still made %d mutating Docker call(s)", n)
	}
}
