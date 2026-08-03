package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mudp/internal/store"
)

// newNetdiskTestApp builds an App with a real store, plus one group and one
// user whose netdisk path points at a fresh temp dir, so netdiskUpload/Copy
// can run end to end through a.userNetdiskRoot without a full server.New().
func newNetdiskTestApp(t *testing.T) (*App, *store.User) {
	t.Helper()
	a := newForwardApp(t)
	if err := a.db.CreateGroup("netdisk-group"); err != nil {
		t.Fatalf("create group: %v", err)
	}
	groups, err := a.db.Groups()
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	var gid int64
	for _, g := range groups {
		if g.Name == "netdisk-group" {
			gid = g.ID
		}
	}
	if gid == 0 {
		t.Fatalf("group netdisk-group not found among %+v", groups)
	}
	if err := a.db.UpdateGroupNetdiskPath(gid, t.TempDir()); err != nil {
		t.Fatalf("set netdisk path: %v", err)
	}
	if err := a.db.CreateUser("alice", "password123", store.RoleUser, []int64{gid}, 10, 0); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := a.db.UserByUsername("alice")
	if err != nil {
		t.Fatalf("user by username: %v", err)
	}
	a.activeTasks = NewActiveTaskRegistry()
	return a, u
}

// adminTasksSnapshot fetches the current task list through the real HTTP
// handler (as the admin panel does), not the registry directly.
func adminTasksSnapshot(t *testing.T, a *App) []ActiveTask {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tasks", nil)
	a.adminTasks(rec, req)
	var out []ActiveTask
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode admin tasks: %v; body=%s", err, rec.Body.String())
	}
	return out
}

// awaitTask polls the admin task list until one matching kind+owner shows up
// or timeout elapses, returning whether it was seen.
func awaitTask(t *testing.T, a *App, kind, owner string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks := adminTasksSnapshot(t, a)
		for i := range tasks {
			if tasks[i].Kind == kind && tasks[i].OwnerName == owner {
				return true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// TestNetdiskUploadTaskVisibleWhileBodyStreaming is a regression test for the
// admin "safe to restart" panel showing empty while a user's upload was still
// in flight: netdiskUpload used to register its ActiveTask only after
// r.ParseMultipartForm had read the entire request body, so for a large
// non-chunked upload the whole data-transfer window -- most of the time the
// browser shows "uploading" -- was invisible to /api/admin/tasks. This drives
// the request body through an io.Pipe so ParseMultipartForm is provably still
// blocked waiting on bytes from the client when the admin task list is
// queried, proving the task is registered before the transfer completes
// rather than after.
func TestNetdiskUploadTaskVisibleWhileBodyStreaming(t *testing.T) {
	a, u := newNetdiskTestApp(t)

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	firstChunkWritten := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		fw, err := mw.CreateFormFile("files", "big.bin")
		if err != nil {
			writeDone <- err
			return
		}
		// The pipe is unbuffered/synchronous: this Write cannot return until the
		// handler side has actually read these bytes, so by the time it returns
		// we know the handler is inside ParseMultipartForm (registration, which
		// happens strictly before that call, has already run).
		if _, err := fw.Write(bytes.Repeat([]byte{0}, 4096)); err != nil {
			writeDone <- err
			return
		}
		close(firstChunkWritten)
		<-release
		if _, err := fw.Write(bytes.Repeat([]byte{0}, 4096)); err != nil {
			writeDone <- err
			return
		}
		if err := mw.Close(); err != nil {
			writeDone <- err
			return
		}
		writeDone <- pw.Close()
	}()

	req := httptest.NewRequest(http.MethodPost, "/api/netdisk/upload", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		a.netdiskUpload(rec, req)
		close(handlerDone)
	}()

	select {
	case <-firstChunkWritten:
	case <-time.After(5 * time.Second):
		t.Fatal("writer goroutine never got its first chunk through the pipe")
	}

	// The handler is now guaranteed to be blocked inside ParseMultipartForm,
	// still waiting for the rest of the body -- exactly the "user is
	// uploading" moment an admin would check before restarting.
	tasks := adminTasksSnapshot(t, a)
	if len(tasks) != 1 || tasks[0].Kind != "netdisk.upload" || tasks[0].OwnerName != "alice" {
		close(release) // don't leak the writer goroutine on failure
		t.Fatalf("expected one visible netdisk.upload task for alice mid-transfer, got %+v", tasks)
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("writer goroutine: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("netdiskUpload did not return in time")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := adminTasksSnapshot(t, a); len(got) != 0 {
		t.Fatalf("task still listed after upload finished: %+v", got)
	}
}

// TestNetdiskCopyTaskVisibleWhileRunning verifies a bulk netdisk copy shows up
// in the admin task list for as long as it is actually copying bytes, and
// disappears the instant it's done. The source file is created via truncate
// (an instant, sparse allocation) so only the copy itself -- real io.Copy
// bytes written to the destination -- has to be slow enough to observe.
func TestNetdiskCopyTaskVisibleWhileRunning(t *testing.T) {
	a, u := newNetdiskTestApp(t)
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		t.Fatalf("netdisk root: %v", err)
	}

	const size = 500 << 20 // 500 MiB, sparse
	srcPath := filepath.Join(root, "big.bin")
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close src: %v", err)
	}

	body := `{"items":[{"from":"big.bin","to":"copied.bin"}],"move":false,"policy":"overwrite"}`
	req := httptest.NewRequest(http.MethodPost, "/api/netdisk/copy", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		a.netdiskCopy(rec, req)
		close(handlerDone)
	}()

	if !awaitTask(t, a, "netdisk.copy", "alice", 5*time.Second) {
		t.Fatal("never observed a netdisk.copy task for alice while the copy was running")
	}

	select {
	case <-handlerDone:
	case <-time.After(15 * time.Second):
		t.Fatal("netdiskCopy did not finish in time")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("copy status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := adminTasksSnapshot(t, a); len(got) != 0 {
		t.Fatalf("task still listed after copy finished: %+v", got)
	}
	dstInfo, err := os.Stat(filepath.Join(root, "copied.bin"))
	if err != nil {
		t.Fatalf("stat copied.bin: %v", err)
	}
	if dstInfo.Size() != size {
		t.Fatalf("copied.bin size = %d, want %d", dstInfo.Size(), size)
	}
}

// TestNetdiskMoveRegistersAndClearsActiveTask covers the move path (same
// handler, req.Move=true, netdisk.move kind). A same-volume move is an
// os.Rename and completes essentially instantly regardless of file size, so
// unlike the copy test above there is no reliable window to catch it mid-
// flight -- this instead checks the full register/run/cleanup cycle leaves no
// stale task behind and that the file actually moved.
func TestNetdiskMoveRegistersAndClearsActiveTask(t *testing.T) {
	a, u := newNetdiskTestApp(t)
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		t.Fatalf("netdisk root: %v", err)
	}
	srcPath := filepath.Join(root, "doc.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	body := `{"items":[{"from":"doc.txt","to":"moved.txt"}],"move":true,"policy":"overwrite"}`
	req := httptest.NewRequest(http.MethodPost, "/api/netdisk/copy", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	rec := httptest.NewRecorder()

	a.netdiskCopy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("move status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := adminTasksSnapshot(t, a); len(got) != 0 {
		t.Fatalf("task still listed after move finished: %+v", got)
	}
	if _, err := os.Stat(srcPath); err == nil {
		t.Fatal("source file still exists after move")
	}
	moved, err := os.ReadFile(filepath.Join(root, "moved.txt"))
	if err != nil || string(moved) != "hello" {
		t.Fatalf("moved.txt = %q, %v", moved, err)
	}
}
