package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFeishuMessagesLifecycle(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	old := time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().Format(time.RFC3339)
	for _, m := range []FeishuMessage{
		{UserID: 1, Kind: FeishuKindProcessWatch, OpenID: "ou_a", Message: "old sent", Status: FeishuMessageSent, CreatedAt: old},
		{UserID: 1, Kind: FeishuKindAdminTest, OpenID: "ou_a", Message: "new failed", Status: FeishuMessageFailed, Error: "boom", CreatedAt: fresh},
		{UserID: 2, Kind: FeishuKindAdminTest, OpenID: "ou_b", Message: "other user", Status: FeishuMessageSent, CreatedAt: fresh},
	} {
		if err := db.AddFeishuMessage(m); err != nil {
			t.Fatalf("AddFeishuMessage: %v", err)
		}
	}

	// The 7-day prune drops only the old row, for every user.
	if err := db.PruneFeishuMessages(time.Now().Add(-7 * 24 * time.Hour)); err != nil {
		t.Fatalf("PruneFeishuMessages: %v", err)
	}
	msgs, err := db.FeishuMessagesForUser(1, 50)
	if err != nil || len(msgs) != 1 || msgs[0].Message != "new failed" || msgs[0].Error != "boom" {
		t.Fatalf("user 1 after prune: err=%v msgs=%+v", err, msgs)
	}
	if msgs2, _ := db.FeishuMessagesForUser(2, 50); len(msgs2) != 1 {
		t.Fatalf("user 2 rows should survive user 1's prune, got %+v", msgs2)
	}

	// Clearing is scoped to one user.
	if err := db.ClearFeishuMessages(1); err != nil {
		t.Fatalf("ClearFeishuMessages: %v", err)
	}
	if msgs, _ := db.FeishuMessagesForUser(1, 50); len(msgs) != 0 {
		t.Fatalf("user 1 history should be empty, got %+v", msgs)
	}
	if msgs2, _ := db.FeishuMessagesForUser(2, 50); len(msgs2) != 1 {
		t.Fatalf("user 2 history should be untouched, got %+v", msgs2)
	}
}
