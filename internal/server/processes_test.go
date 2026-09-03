package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mudp/internal/auth"
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
	if err := db.CreateUser("alice", "alice-password-1", store.RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	var user store.User
	for _, u := range mustUsers(t, db) {
		if u.Username == "alice" {
			user = u
		}
	}
	// Link a Feishu identity so the open_id notification branch runs. Feishu
	// itself is left unconfigured, so the bot send fails and is only logged —
	// the in-app notification must still go out.
	if err := db.UpdateFeishuProfile(user.ID, auth.FeishuUser{OpenID: "ou_alice", Name: "Alice"}); err != nil {
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

	// The process disappears: watch fires, the notification goes out.
	delete(procs, "123")
	a.watchProcesses(context.Background())
	if watches, _ = db.ProcessWatches(); len(watches) != 0 {
		t.Fatalf("watch should be deleted after firing, got %d", len(watches))
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
	if err := db.CreateUser("bob", "bob-password-12", store.RoleUser, 0, 5, 0); err != nil {
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

func TestSendFeishuTextRequiresConfig(t *testing.T) {
	db := newServerTestDB(t)
	a := &App{db: db}
	if err := a.sendFeishuText(1, "ou_x", store.FeishuKindAdminTest, "hi"); err == nil {
		t.Fatal("sendFeishuText without a configured app should fail")
	}
	// The failed attempt must land in the send history.
	msgs, err := db.FeishuMessagesForUser(1, 10)
	if err != nil || len(msgs) != 1 || msgs[0].Status != store.FeishuMessageFailed {
		t.Fatalf("expected one failed history row, got err=%v msgs=%+v", err, msgs)
	}
}
