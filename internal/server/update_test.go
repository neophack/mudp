package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"mudp/internal/version"
)

func withStubGitHub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("update check must send a User-Agent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	old := githubReleasesURL
	githubReleasesURL = srv.URL
	t.Cleanup(func() {
		githubReleasesURL = old
		srv.Close()
	})
	return srv
}

func TestUpdateCheckNewerRelease(t *testing.T) {
	withStubGitHub(t, http.StatusOK, `{"tag_name":"v9.9.9","name":"v9.9.9","body":"## Changes\n- fix a\n- add b\n","published_at":"2026-08-01T00:00:00Z"}`)
	oldVersion := version.Version
	version.Version = "v1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })

	app := &App{}
	rec := httptest.NewRecorder()
	app.updateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/update/check", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var res updateCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Current != "v1.0.0" || res.Latest != "v9.9.9" || !res.Available {
		t.Fatalf("unexpected response: %+v", res)
	}
	wantWin := "https://github.com/neophack/mudp/releases/download/v9.9.9/mudp-windows-amd64-v9.9.9.zip"
	if res.Downloads["windows"] != wantWin {
		t.Errorf("windows download = %q, want %q", res.Downloads["windows"], wantWin)
	}
	wantLinux := "https://github.com/neophack/mudp/releases/download/v9.9.9/mudp-linux-amd64-v9.9.9.tar.gz"
	if res.Downloads["linux"] != wantLinux {
		t.Errorf("linux download = %q, want %q", res.Downloads["linux"], wantLinux)
	}
	// The release body and publish time feed the update window's "what's new"
	// list; the body is trimmed but otherwise passed through verbatim.
	if res.Notes != "## Changes\n- fix a\n- add b" {
		t.Errorf("notes = %q", res.Notes)
	}
	if res.ReleasedAt != "2026-08-01T00:00:00Z" {
		t.Errorf("releasedAt = %q", res.ReleasedAt)
	}

	// Second call must be served from the cache: kill the stub, ask again.
	rec2 := httptest.NewRecorder()
	app.updateCheck(rec2, httptest.NewRequest(http.MethodGet, "/api/update/check", nil))
	var res2 updateCheckResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.Latest != "v9.9.9" {
		t.Fatalf("cached lookup lost the tag: %+v", res2)
	}
}

func TestUpdateCheckDevNeverUpdates(t *testing.T) {
	withStubGitHub(t, http.StatusOK, `{"tag_name":"v9.9.9"}`)
	oldVersion := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = oldVersion })

	app := &App{}
	rec := httptest.NewRecorder()
	app.updateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/update/check", nil))
	var res updateCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Fatal("dev build must not report an update available")
	}
}

func TestUpdateCheckRefreshBypassesCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		tag := "v1.0.0"
		if atomic.LoadInt32(&hits) > 1 {
			tag = "v1.2.0" // the publisher cut a new release between checks
		}
		w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	defer srv.Close()
	old := githubReleasesURL
	githubReleasesURL = srv.URL
	t.Cleanup(func() { githubReleasesURL = old })
	oldVersion := version.Version
	version.Version = "v1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })

	app := &App{}
	get := func(url string) updateCheckResponse {
		rec := httptest.NewRecorder()
		app.updateCheck(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var res updateCheckResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := get("/api/update/check"); res.Latest != "v1.0.0" {
		t.Fatalf("first check latest = %q", res.Latest)
	}
	// Within the cache TTL a plain check must not hit GitHub again…
	if res := get("/api/update/check"); res.Latest != "v1.0.0" || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("cached check should not re-fetch (latest=%q hits=%d)", res.Latest, hits)
	}
	// …but the manual refresh button does, picking up the new release.
	if res := get("/api/update/check?refresh=1"); res.Latest != "v1.2.0" || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("refresh should bypass the cache (latest=%q hits=%d)", res.Latest, hits)
	}
	// The refreshed result becomes the new cached answer.
	if res := get("/api/update/check"); res.Latest != "v1.2.0" || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("refresh should update the cache (latest=%q hits=%d)", res.Latest, hits)
	}
}

func TestUpdateCheckRefreshRetriesAfterError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0"}`))
	}))
	defer srv.Close()
	old := githubReleasesURL
	githubReleasesURL = srv.URL
	t.Cleanup(func() { githubReleasesURL = old })

	app := &App{}
	get := func(url string) updateCheckResponse {
		rec := httptest.NewRecorder()
		app.updateCheck(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var res updateCheckResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := get("/api/update/check"); res.Error == "" {
		t.Fatalf("first check should surface the upstream error, got %+v", res)
	}
	// The error is cached (with its shorter TTL): a plain check must not retry.
	if res := get("/api/update/check"); res.Error == "" || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("cached error should not re-fetch (res=%+v hits=%d)", res, hits)
	}
	// Refresh ignores the cached error and retries, finding the new release.
	if res := get("/api/update/check?refresh=1"); res.Error != "" || res.Latest != "v1.2.0" || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("refresh should retry past a cached error (res=%+v hits=%d)", res, hits)
	}
	// The recovered result replaces the cached error.
	if res := get("/api/update/check"); res.Error != "" || res.Latest != "v1.2.0" || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("success should be cached over the old error (res=%+v hits=%d)", res, hits)
	}
}

func TestUpdateCheckOnlyRefreshOneBypassesCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()
	old := githubReleasesURL
	githubReleasesURL = srv.URL
	t.Cleanup(func() { githubReleasesURL = old })

	app := &App{}
	for _, url := range []string{
		"/api/update/check",
		"/api/update/check?refresh=0",
		"/api/update/check?refresh=true",
		"/api/update/check?refresh=",
	} {
		app.updateCheck(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, url, nil))
	}
	if hits != 1 {
		t.Fatalf("only refresh=1 may bypass the cache, upstream hits = %d", hits)
	}
}

func TestUpdateCheckUpstreamError(t *testing.T) {
	withStubGitHub(t, http.StatusForbidden, `{"message":"rate limited"}`)

	app := &App{}
	rec := httptest.NewRecorder()
	app.updateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors ride in the body)", rec.Code)
	}
	var res updateCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || res.Available {
		t.Fatalf("expected an error and no update flag, got %+v", res)
	}
}
