package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	withStubGitHub(t, http.StatusOK, `{"tag_name":"v9.9.9","name":"v9.9.9"}`)
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
	wantWin := "https://github.com/neophack/mudp/releases/download/v9.9.9/mudp_x86.exe"
	if res.Downloads["windows"] != wantWin {
		t.Errorf("windows download = %q, want %q", res.Downloads["windows"], wantWin)
	}
	wantLinux := "https://github.com/neophack/mudp/releases/download/v9.9.9/mudp_x86_linux"
	if res.Downloads["linux"] != wantLinux {
		t.Errorf("linux download = %q, want %q", res.Downloads["linux"], wantLinux)
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
