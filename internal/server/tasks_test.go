package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mudp/internal/store"
)

// TestActiveTaskRegistryLifecycle verifies a task is visible in the snapshot
// while in flight and disappears the instant its done func runs -- the core
// property the admin task card relies on to answer "is anything running now".
func TestActiveTaskRegistryLifecycle(t *testing.T) {
	reg := NewActiveTaskRegistry()
	task, done := reg.start("netdisk.copy", "alice · 3 item(s)", &store.User{ID: 1, Username: "alice"})
	task.setTotal(3)
	task.addDone(1)

	snap := reg.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Progress != 33 || snap[0].OwnerName != "alice" {
		t.Fatalf("unexpected snapshot: progress=%d owner=%s", snap[0].Progress, snap[0].OwnerName)
	}

	done()
	if len(reg.snapshot()) != 0 {
		t.Fatalf("task still present after done()")
	}
}

// TestAdminTasksMergesAllSources checks that the /api/admin/tasks handler
// combines in-flight ActiveTasks, running backup jobs (skipping finished
// ones), and chunk-upload sessions into one list.
func TestAdminTasksMergesAllSources(t *testing.T) {
	a := newForwardApp(t)
	a.activeTasks = NewActiveTaskRegistry()
	_, done := a.activeTasks.start("netdisk.copy", "alice copy", &store.User{ID: 1, Username: "alice"})
	defer done()

	a.backupJobs = NewBackupJobRegistry()
	a.backupJobs.add(&BackupJob{ID: "running-job", Kind: "backup.run", Status: "running", StartedAt: time.Now(), OwnerID: 2, OwnerName: "bob"})
	a.backupJobs.add(&BackupJob{ID: "done-job", Kind: "backup.run", Status: "done", StartedAt: time.Now(), OwnerID: 2, OwnerName: "bob"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tasks", nil)
	a.adminTasks(rec, req)

	var out []ActiveTask
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("got %d tasks, want 2 (one active-task, one running backup job): %s", len(out), rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "done-job") {
		t.Fatalf("finished backup job leaked into the admin task list: %s", body)
	}
	if !strings.Contains(body, "running-job") || !strings.Contains(body, "alice copy") {
		t.Fatalf("missing expected tasks in: %s", body)
	}
}

// TestUserTasksGatesByAgeAndOwner is a regression for the "10 seconds"
// requirement: a task younger than taskVisibleAfter must not appear in the
// caller's own panel (routine fast copies shouldn't flash a job row), and a
// non-admin must never see another user's task even once it ages in.
func TestUserTasksGatesByAgeAndOwner(t *testing.T) {
	a := newForwardApp(t)
	a.activeTasks = NewActiveTaskRegistry()

	young, doneYoung := a.activeTasks.start("netdisk.copy", "alice young", &store.User{ID: 1, Username: "alice"})
	defer doneYoung()
	_ = young // stays at StartedAt = now, i.e. under the threshold

	oldMine, doneOldMine := a.activeTasks.start("netdisk.copy", "alice old", &store.User{ID: 1, Username: "alice"})
	oldMine.StartedAt = time.Now().Add(-20 * time.Second)
	defer doneOldMine()

	oldOther, doneOldOther := a.activeTasks.start("netdisk.copy", "bob old", &store.User{ID: 2, Username: "bob"})
	oldOther.StartedAt = time.Now().Add(-20 * time.Second)
	defer doneOldOther()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey, &store.User{ID: 1, Username: "alice", Role: store.RoleUser}))
	a.userTasks(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "alice young") {
		t.Fatalf("task younger than the visibility threshold leaked into the panel: %s", body)
	}
	if !strings.Contains(body, "alice old") {
		t.Fatalf("alice's own aged task is missing: %s", body)
	}
	if strings.Contains(body, "bob old") {
		t.Fatalf("alice saw bob's task: %s", body)
	}
}

// TestChunkUploadRegistrySnapshot verifies progress is derived from the
// on-disk resume state, and that a session whose state file has been removed
// (upload completed/aborted through some other path) is dropped silently.
func TestChunkUploadRegistrySnapshot(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "big.bin")
	st := &chunkUploadState{Size: 100, ChunkSize: 25, TotalChunks: 4, Received: map[int]bool{0: true, 1: true}}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}

	reg := NewChunkUploadRegistry()
	reg.start(dst, "big.bin", 100, &store.User{ID: 5, Username: "carol"})

	snap := reg.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Done != 50 || snap[0].Total != 100 || snap[0].Progress != 50 || snap[0].OwnerName != "carol" {
		t.Fatalf("unexpected snapshot: done=%d total=%d progress=%d owner=%s", snap[0].Done, snap[0].Total, snap[0].Progress, snap[0].OwnerName)
	}

	reg.finish(dst)
	if len(reg.snapshot()) != 0 {
		t.Fatalf("session still present after finish()")
	}

	// A session whose state file vanished out-of-band (e.g. completed via a
	// different code path) must be dropped rather than shown forever.
	reg.start(dst, "big.bin", 100, nil)
	_ = os.Remove(chunkStatePath(dst))
	if len(reg.snapshot()) != 0 {
		t.Fatalf("session with no resume state should not be reported active")
	}
}
