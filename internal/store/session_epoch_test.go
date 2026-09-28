package store

import (
	"testing"
)

// TestSessionEpochRevokesSessions pins the revocation contract: every
// credential change bumps users.session_epoch, so a session cookie minted
// before the change (carrying the old epoch) stops matching on the next
// request. Authenticate and UserByID must both surface the current epoch.
func TestSessionEpochRevokesSessions(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("dave", "start-pass-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("dave", "start-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if u.SessionEpoch != 0 {
		t.Fatalf("fresh account epoch = %d, want 0", u.SessionEpoch)
	}
	stored, err := db.UserByID(u.ID)
	if err != nil || stored.SessionEpoch != 0 {
		t.Fatalf("UserByID epoch = %d (err %v), want 0", stored.SessionEpoch, err)
	}

	// Admin reset: the epoch must move, invalidating cookies issued against 0.
	if err := db.UpdateUser(u.ID, "reset-pass-456", "", 0, nil, nil); err != nil {
		t.Fatalf("admin reset: %v", err)
	}
	stored, _ = db.UserByID(u.ID)
	if stored.SessionEpoch != 1 {
		t.Fatalf("epoch after admin reset = %d, want 1", stored.SessionEpoch)
	}
	// Unrelated updates (role/cap changes) must not bump the epoch.
	if err := db.UpdateUser(u.ID, "", RoleOperator, 20, nil, nil); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.UserByID(u.ID)
	if stored.SessionEpoch != 1 {
		t.Fatalf("epoch after role/cap change = %d, want unchanged 1", stored.SessionEpoch)
	}
}

func TestChangeOwnPassword(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("erin", "current-pass-1", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("erin", "current-pass-1")

	cases := []struct {
		name    string
		current string
		next    string
		wantErr bool
	}{
		{"wrong current password", "wrong-pass-999", "new-pass-12345", true},
		{"too-short new password", "current-pass-1", "short", true},
		{"success", "current-pass-1", "new-pass-12345", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.ChangeOwnPassword(u.ID, c.current, c.next)
			if (err != nil) != c.wantErr {
				t.Fatalf("ChangeOwnPassword err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}

	// After the successful case the new credential works, the old one does
	// not, and the epoch moved on (so the pre-change cookie is stale).
	if _, err := db.Authenticate("erin", "current-pass-1"); err == nil {
		t.Error("old password still authenticates after self-change")
	}
	fresh, err := db.Authenticate("erin", "new-pass-12345")
	if err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if fresh.SessionEpoch != 1 {
		t.Fatalf("epoch after self-change = %d, want 1", fresh.SessionEpoch)
	}
}

// TestChangeOwnPasswordSSOOnly verifies fail-closed behavior for accounts with
// no usable password: there is nothing to verify the current password against,
// so the change is refused instead of silently minting a password login.
func TestChangeOwnPasswordSSOOnly(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("frank", "sso-pass-1234", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("frank", "sso-pass-1234")
	if _, err := db.Exec(`update users set password_hash=? where id=?`, NoPasswordSentinel, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ChangeOwnPassword(u.ID, "whatever", "new-pass-12345"); err == nil {
		t.Fatal("SSO-only account accepted a password change")
	}
}
