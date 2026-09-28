package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mudp/internal/auth"
	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// seedScopedCache fills the runtime cache with a platform system snapshot, a
// scope basis and a container list spanning two owners, so scopedSystem can be
// proven to derive everything in memory (the docker client points at a dead
// port — any daemon call fails loudly).
func seedScopedCache(a *App) {
	a.cacheMu.Lock()
	a.cachedSystem = dockerx.SystemInfo{Healthy: true, DockerVer: "27.0", Networks: 5}
	a.cachedBasis = dockerx.ScopeBasis{
		ImageSizes: map[string]int64{
			"imgA": 100 << 20,
			"imgB": 200 << 20,
		},
		Volumes: []dockerx.VolumeScope{
			{Owner: "alice", SizeB: 5 << 20},
			{Owner: "bob", SizeB: 7 << 20},
		},
		Networks: []dockerx.NetworkScope{
			{System: true}, {System: true}, {System: true},
			{Managed: true, Owner: "alice"},
			{Managed: true, Owner: "bob"},
		},
	}
	a.cachedContainers = []dockerx.Container{
		{ID: "a1", FullName: "/mudp-alice-app", State: "running", Health: "healthy", ImageID: "imgA",
			Labels: map[string]string{"mudp.user": "alice"}},
		{ID: "a2", FullName: "/mudp-alice-db", State: "exited", Health: "unhealthy", ImageID: "imgB",
			Labels: map[string]string{"mudp.user": "alice"}},
		{ID: "b1", FullName: "/mudp-bob-web", State: "running", ImageID: "imgA",
			Labels: map[string]string{"mudp.user": "bob"}},
	}
	a.cacheMu.Unlock()
}

func TestScopedSystemDerivesFromCache(t *testing.T) {
	a := newForwardApp(t)
	seedScopedCache(a)

	alice := a.scopedSystem("alice")
	if alice.DockerVer != "27.0" || !alice.Healthy {
		t.Fatal("host fields must come from the cached platform snapshot")
	}
	if alice.Containers.Total != 2 || alice.Containers.Running != 1 || alice.Containers.Stopped != 1 {
		t.Errorf("alice containers = %+v, want total 2 running 1 stopped 1", alice.Containers)
	}
	if alice.Containers.Healthy != 1 || alice.Containers.Unhealthy != 1 {
		t.Errorf("alice health rollup = %+v, want healthy 1 unhealthy 1", alice.Containers)
	}
	// Both referenced images count, with their combined basis sizes.
	if alice.Images.Count != 2 || alice.Images.SizeB != 300<<20 {
		t.Errorf("alice images = %+v, want 2 images at 300 MiB", alice.Images)
	}
	if alice.Volumes.Count != 1 || alice.Volumes.SizeB != 5<<20 {
		t.Errorf("alice volumes = %+v, want her single 5 MiB volume", alice.Volumes)
	}
	// System networks are everyone's; bob's managed network is not.
	if alice.Networks != 4 {
		t.Errorf("alice networks = %d, want 3 system + her 1", alice.Networks)
	}

	bob := a.scopedSystem("bob")
	if bob.Containers.Total != 1 || bob.Containers.Running != 1 {
		t.Errorf("bob containers = %+v, want total 1 running 1", bob.Containers)
	}
	if bob.Containers.Healthy != 0 || bob.Containers.Unhealthy != 0 {
		t.Errorf("bob health rollup = %+v, want no health statuses", bob.Containers)
	}
	if bob.Images.Count != 1 || bob.Images.SizeB != 100<<20 {
		t.Errorf("bob images = %+v, want the single distinct imgA", bob.Images)
	}
	if bob.Volumes.Count != 1 || bob.Volumes.SizeB != 7<<20 {
		t.Errorf("bob volumes = %+v, want his single 7 MiB volume", bob.Volumes)
	}
	if bob.Networks != 4 {
		t.Errorf("bob networks = %d, want 3 system + his 1", bob.Networks)
	}
}

// TestAuthMiddlewareRejectsStaleEpoch pins the session-revocation gate: a
// cookie whose epoch trails the user's current one is refused exactly like a
// missing cookie, while a fresh one passes.
func TestAuthMiddlewareRejectsStaleEpoch(t *testing.T) {
	a := newForwardApp(t)
	a.auth = auth.New("test-secret")
	if err := a.db.CreateUser("gina", "login-pass-123", store.RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := a.db.Authenticate("gina", "login-pass-123")
	if err != nil {
		t.Fatal(err)
	}

	handler := a.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP

	mint := func(epoch int64) string {
		rec := httptest.NewRecorder()
		a.auth.Set(rec, httptest.NewRequest(http.MethodGet, "/", nil), u.ID, epoch)
		return rec.Result().Cookies()[0].Value
	}
	attach := func(value string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: value})
		return req
	}

	// Current epoch passes.
	rec := httptest.NewRecorder()
	handler(rec, attach(mint(u.SessionEpoch)))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh cookie rejected: %d %s", rec.Code, rec.Body.String())
	}

	// An epoch behind the stored one (the admin reset the password since the
	// cookie was issued) is refused.
	rec = httptest.NewRecorder()
	handler(rec, attach(mint(u.SessionEpoch-1)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale-epoch cookie accepted: %d %s", rec.Code, rec.Body.String())
	}

	// ...and after the reset the handler keeps refusing the pre-reset cookie
	// even though its signature and expiry are still fine.
	if err := a.db.UpdateUser(u.ID, "rotated-pass-123", "", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handler(rec, attach(mint(u.SessionEpoch)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-reset cookie accepted after password reset: %d %s", rec.Code, rec.Body.String())
	}
}

// TestUserPasswordChange pins the self-service rotation endpoint: wrong
// current password rejected, success re-issues a cookie carrying the new
// epoch, and the pre-change cookie for the same account stops working.
func TestUserPasswordChange(t *testing.T) {
	a := newForwardApp(t)
	a.auth = auth.New("test-secret")
	if err := a.db.CreateUser("henry", "old-pass-12345", store.RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := a.db.Authenticate("henry", "old-pass-12345")
	if err != nil {
		t.Fatal(err)
	}

	call := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/user/password", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userKey, u))
		a.userPasswordChange(rec, req)
		return rec
	}

	if rec := call(`{"current":"nope","new":"brand-new-1234"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(`{"current":"old-pass-12345","new":"brand-new-1234"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change rejected: %d %s", rec.Code, rec.Body.String())
	}

	// The re-issued cookie carries the new epoch and authenticates...
	if _, epoch, ok := a.auth.UserID(requestWithSession(rec.Result().Cookies()[0].Value)); !ok || epoch != u.SessionEpoch+1 {
		t.Fatalf("re-issued cookie epoch mismatch (ok=%v)", ok)
	}
	// ...while a cookie from before the change is now dead.
	stale := httptest.NewRecorder()
	a.auth.Set(stale, httptest.NewRequest(http.MethodGet, "/", nil), u.ID, u.SessionEpoch)
	handler := a.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP
	guarded := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	guarded.AddCookie(stale.Result().Cookies()[0])
	denied := httptest.NewRecorder()
	handler(denied, guarded)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("pre-change cookie still authenticates: %d", denied.Code)
	}
	// The new credential works for future logins.
	if _, err := a.db.Authenticate("henry", "brand-new-1234"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

// requestWithSession attaches a session cookie value to a fresh request.
func requestWithSession(value string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: value})
	return req
}
