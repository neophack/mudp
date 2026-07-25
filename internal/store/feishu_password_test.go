package store

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMigrationRevokesLegacyFeishuPasswords simulates a database written by the
// vulnerable build (password_hash = bcrypt(open_id)) and confirms the migration
// takes the derivable credential away while leaving a genuine one intact.
func TestMigrationRevokesLegacyFeishuPasswords(t *testing.T) {
	db := newTestDB(t)

	const vulnOpenID = "ou_legacy1234"
	vulnHash, _ := bcrypt.GenerateFromPassword([]byte(vulnOpenID), bcrypt.DefaultCost)
	realHash, _ := bcrypt.GenerateFromPassword([]byte("chosen-by-admin"), bcrypt.DefaultCost)

	// Row as the old code would have written it.
	if _, err := db.Exec(`insert into users(username,password_hash,role,container_cap,port_prefix,created_at,feishu_open_id,display_name)
		values(?,?,?,?,?,?,?,?)`, "oulegacy1234", string(vulnHash), "user", 10, 301, "2026-01-01T00:00:00Z", vulnOpenID, "Legacy"); err != nil {
		t.Fatal(err)
	}
	// An SSO user an admin later gave a real password.
	if _, err := db.Exec(`insert into users(username,password_hash,role,container_cap,port_prefix,created_at,feishu_open_id,display_name)
		values(?,?,?,?,?,?,?,?)`, "ouother", string(realHash), "user", 10, 302, "2026-01-01T00:00:00Z", "ou_other", "Other"); err != nil {
		t.Fatal(err)
	}

	// Pre-condition: the vulnerability is live on this row.
	if _, err := db.Authenticate("oulegacy1234", vulnOpenID); err != nil {
		t.Fatalf("setup: legacy row should be exploitable before the migration, got %v", err)
	}

	if err := migrateRevokeFeishuDerivedPasswords(db.DB); err != nil {
		t.Fatalf("migration: %v", err)
	}

	if _, err := db.Authenticate("oulegacy1234", vulnOpenID); err == nil {
		t.Error("open ID still authenticates after the migration")
	}
	if _, err := db.Authenticate("ouother", "chosen-by-admin"); err != nil {
		t.Errorf("admin-set password was wrongly revoked: %v", err)
	}
}

// TestPasswordPolicyEnforced pins the minimum on every path that hashes a
// password, so a weak credential cannot be introduced through account creation
// or a later password change.
func TestPasswordPolicyEnforced(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("weakuser", "short", RoleUser, nil, 5, 0); err == nil {
		t.Error("CreateUser accepted a password below the minimum")
	}
	if err := db.CreateUser("gooduser", "long-enough-password", RoleUser, nil, 5, 0); err != nil {
		t.Fatalf("CreateUser rejected a compliant password: %v", err)
	}
	u, err := db.Authenticate("gooduser", "long-enough-password")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := db.UpdateUser(u.ID, "short", "", 0, nil, nil); err == nil {
		t.Error("UpdateUser accepted a password below the minimum")
	}
	// The old password must still work: a rejected change must not partially apply.
	if _, err := db.Authenticate("gooduser", "long-enough-password"); err != nil {
		t.Errorf("original password stopped working after a rejected change: %v", err)
	}
	// An empty password still means "leave unchanged", not "set an empty one".
	if err := db.UpdateUser(u.ID, "", RoleOperator, 0, nil, nil); err != nil {
		t.Errorf("empty password should mean unchanged: %v", err)
	}
	if _, err := db.Authenticate("gooduser", "long-enough-password"); err != nil {
		t.Errorf("password changed when it should not have: %v", err)
	}
}
