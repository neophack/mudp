package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// TestContainerTerminalRequiresMutate is the P0-3 regression for
// server.go:1841: a readonly-role caller must be rejected before the handler
// ever reaches the WebSocket handshake / docker exec attach, since those steps
// would hand a readonly session an interactive root shell.
func TestContainerTerminalRequiresMutate(t *testing.T) {
	a := newForwardApp(t)
	u := &store.User{ID: 1, Username: "ro", Role: store.RoleReadonly}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/containers/terminal?id=deadbeef", nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	a.containerTerminal(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("containerTerminal for readonly = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateStreamRequiresMutate is the P0-3 regression for server.go:1207: a
// readonly-role caller must not be able to create a container via the SSE
// creation endpoint (the plain /api/containers POST already enforces this).
func TestCreateStreamRequiresMutate(t *testing.T) {
	a := newForwardApp(t)
	u := &store.User{ID: 1, Username: "ro", Role: store.RoleReadonly}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/containers/create/stream", strings.NewReader(`{"name":"x","image":"y"}`))
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	a.createStream(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("createStream for readonly = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestBackupJobsListFiltersByOwner is the P0-3 regression for backup.go:589:
// a non-admin must only ever see their own backup jobs, never another user's.
func TestBackupJobsListFiltersByOwner(t *testing.T) {
	a := newForwardApp(t)
	a.backupJobs = NewBackupJobRegistry()
	a.backupJobs.add(&BackupJob{ID: "alice-job", Status: "running", StartedAt: time.Now(), OwnerID: 1, OwnerName: "alice"})
	a.backupJobs.add(&BackupJob{ID: "bob-job", Status: "running", StartedAt: time.Now(), OwnerID: 2, OwnerName: "bob"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/backup/jobs", nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey, &store.User{ID: 1, Username: "alice", Role: store.RoleUser}))
	a.backupJobsList(rec, req)
	if !strings.Contains(rec.Body.String(), "alice-job") || strings.Contains(rec.Body.String(), "bob-job") {
		t.Fatalf("alice's job list leaked another user's job: %s", rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/backup/jobs", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), userKey, &store.User{ID: 99, Username: "admin", Role: store.RoleAdmin}))
	a.backupJobsList(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "alice-job") || !strings.Contains(rec2.Body.String(), "bob-job") {
		t.Fatalf("admin job list missing an owner's job: %s", rec2.Body.String())
	}
}

// TestBackupJobCancelForbidsOtherOwner is the P0-3 regression for
// backup.go:1002: a non-admin must not be able to cancel another user's
// running backup job.
func TestBackupJobCancelForbidsOtherOwner(t *testing.T) {
	a := newForwardApp(t)
	a.backupJobs = NewBackupJobRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &BackupJob{ID: "bob-job", Status: "running", StartedAt: time.Now(), OwnerID: 2, OwnerName: "bob"}
	job.cancel = func() {}
	a.backupJobs.add(job)
	_ = ctx

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/jobs/cancel", strings.NewReader(`{"id":"bob-job"}`))
	req = req.WithContext(context.WithValue(req.Context(), userKey, &store.User{ID: 1, Username: "alice", Role: store.RoleUser}))
	a.backupJobCancel(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("alice cancelling bob's job = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if a.backupJobs.get("bob-job").snapshot().Status != "running" {
		t.Fatal("bob's job was cancelled by another user despite the 403")
	}
}

// TestImagePresetResolveRespectsGroupVisibility is the P0-3 regression for
// images_ext.go:593: an activated user must not be able to read another
// group's image preset (which can carry a static, admin-set password/value)
// by guessing or enumerating image ids.
func TestImagePresetResolveRespectsGroupVisibility(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.CreateGroup("team-a"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	groups, _ := db.Groups()
	var teamA int64
	for _, g := range groups {
		if g.Name == "team-a" {
			teamA = g.ID
		}
	}
	if err := db.CreateUser("outsider", "x-valid-1234", store.RoleUser, 0, 0, 0); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	outsider, _ := db.UserByUsername("outsider")

	if err := db.SaveImage("secret-img", "mudp-secret", "secret:latest"); err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	admin, _ := db.UserByUsername("admin")
	imgs, _ := db.ImagesForUser(admin.ID, true)
	var secretID int64
	for _, img := range imgs {
		if img.DisplayName == "secret-img" {
			secretID = img.ID
		}
	}
	if secretID == 0 {
		t.Fatal("failed to resolve secret-img id")
	}
	if err := db.SetImageGroups(secretID, []int64{teamA}); err != nil {
		t.Fatalf("SetImageGroups: %v", err)
	}
	if err := db.SetImagePreset(secretID, &store.ImagePreset{Env: []string{"TOKEN=super-secret"}}); err != nil {
		t.Fatalf("SetImagePreset: %v", err)
	}

	dc, err := dockerx.NewWithHost("tcp://127.0.0.1:1")
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { dc.Close() })
	a := &App{db: db, docker: dc}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/images/preset/resolve", strings.NewReader(`{"imageId":`+strconv.FormatInt(secretID, 10)+`}`))
	req = req.WithContext(context.WithValue(req.Context(), userKey, outsider))
	a.imagePresetResolve(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("outsider resolved a preset for an image outside their group: %s", rec.Body.String())
	}
}
