package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mudp/internal/store"
)

func newServerTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func mustUsers(t *testing.T, db *store.DB) []store.User {
	t.Helper()
	users, err := db.Users()
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	return users
}

func mustNotifications(t *testing.T, db *store.DB, userID int64) []store.Notification {
	t.Helper()
	notes, _, err := db.NotificationsForUser(userID, 50)
	if err != nil {
		t.Fatalf("NotificationsForUser: %v", err)
	}
	return notes
}

func TestWatchProcessesFiresOnExit(t *testing.T) {
	db := newServerTestDB(t)
	if err := db.CreateUser("alice", "alice-password-1", store.RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	var user store.User
	for _, u := range mustUsers(t, db) {
		if u.Username == "alice" {
			user = u
		}
	}

	// Feishu webhook points at a stub so the send path is exercised too.
	var feishuHits int32
	feishu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&feishuHits, 1)
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer feishu.Close()
	if err := db.UpdateUserFeishuWebhook(user.ID, feishu.URL); err != nil {
		t.Fatal(err)
	}

	procs := map[string]string{"123": "python train.py", "456": "sleep 100"}
	a := &App{db: db}
	a.processProbe = func(ctx context.Context, containerID string) (map[string]string, error) {
		return procs, nil
	}

	if _, err := db.AddProcessWatch(store.ProcessWatch{
		UserID: user.ID, ContainerID: "cid", ContainerName: "web", PID: "123", Command: "python train.py",
	}); err != nil {
		t.Fatal(err)
	}

	// Still running: nothing fires.
	a.watchProcesses(context.Background())
	watches, err := db.ProcessWatches()
	if err != nil || len(watches) != 1 {
		t.Fatalf("watch should survive while the process runs: %v %d", err, len(watches))
	}

	// The process disappears: watch fires, notifications go out.
	delete(procs, "123")
	a.watchProcesses(context.Background())
	if watches, _ = db.ProcessWatches(); len(watches) != 0 {
		t.Fatalf("watch should be deleted after firing, got %d", len(watches))
	}
	if atomic.LoadInt32(&feishuHits) != 1 {
		t.Fatalf("feishu webhook hits = %d, want 1", feishuHits)
	}
	notes := mustNotifications(t, db, user.ID)
	if len(notes) != 1 || notes[0].Type != store.NotificationProcessFinished {
		t.Fatalf("expected one process_finished notification, got %+v", notes)
	}
	if !strings.Contains(notes[0].Message, "python train.py") {
		t.Fatalf("notification should name the command: %q", notes[0].Message)
	}
}

func TestWatchProcessesContainerGone(t *testing.T) {
	db := newServerTestDB(t)
	if err := db.CreateUser("bob", "bob-password-12", store.RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	var user store.User
	for _, u := range mustUsers(t, db) {
		if u.Username == "bob" {
			user = u
		}
	}

	gone := int32(0)
	a := &App{db: db}
	a.processProbe = func(ctx context.Context, containerID string) (map[string]string, error) {
		if atomic.LoadInt32(&gone) == 1 {
			// A stopped/removed container, distinct from a daemon hiccup.
			return nil, errors.New("Error response from daemon: No such container")
		}
		return map[string]string{"7": "bash"}, nil
	}
	if _, err := db.AddProcessWatch(store.ProcessWatch{
		UserID: user.ID, ContainerID: "cid2", ContainerName: "box", PID: "7", Command: "bash",
	}); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt32(&gone, 1)
	a.watchProcesses(context.Background())
	if watches, _ := db.ProcessWatches(); len(watches) != 0 {
		t.Fatalf("watch on a stopped container should fire and be removed")
	}
	notes := mustNotifications(t, db, user.ID)
	if len(notes) != 1 || !strings.Contains(notes[0].Message, "container stopped or removed") {
		t.Fatalf("unexpected notifications: %+v", notes)
	}
}

func TestSendFeishuTextRejectsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer srv.Close()
	if err := sendFeishuText(srv.URL, "hi"); err == nil || !strings.Contains(err.Error(), "19021") {
		t.Fatalf("expected Feishu error-body rejection, got %v", err)
	}
}

func TestNormalizeFeishuWebhook(t *testing.T) {
	valid := "https://open.feishu.cn/open-apis/bot/v2/hook/abc"
	if got := normalizeFeishuWebhook(" " + valid + " "); got != valid {
		t.Fatalf("normalize = %q, want %q", got, valid)
	}
	if got := normalizeFeishuWebhook(""); got != "" {
		t.Fatalf("empty should stay empty, got %q", got)
	}
	for _, bad := range []string{
		"http://open.feishu.cn/open-apis/bot/v2/hook/abc",
		"https://evil.example.com/open-apis/bot/v2/hook/abc",
		"https://open.feishu.cn/other",
	} {
		if got := normalizeFeishuWebhook(bad); got != "" {
			t.Fatalf("normalize(%q) = %q, want \"\"", bad, got)
		}
	}
}
