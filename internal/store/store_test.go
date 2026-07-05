package store

import (
	"path/filepath"
	"testing"
)

// newTestDB opens an in-file SQLite DB in the test temp dir. Each test gets its
// own file so there is no cross-test contention.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "secret"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestValidRole(t *testing.T) {
	for _, r := range []string{RoleAdmin, RoleOperator, RoleHelpdesk, RoleReadonly, RoleUser} {
		if !ValidRole(r) {
			t.Errorf("ValidRole(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"", "superadmin", "ADMIN", "guest"} {
		if ValidRole(r) {
			t.Errorf("ValidRole(%q) = true, want false", r)
		}
	}
}

func TestMigrateWidensRoleConstraint(t *testing.T) {
	db := newTestDB(t)
	// After migration an operator-role insert must succeed (the old CHECK
	// would have rejected it). Insert directly to bypass ValidRole.
	for _, role := range []string{RoleOperator, RoleHelpdesk, RoleReadonly} {
		if _, err := db.Exec(`insert into users(username,password_hash,role,created_at) values(?,?,?,datetime('now'))`,
			"u-"+role, "x", role); err != nil {
			t.Fatalf("insert role %q: %v", role, err)
		}
	}
	// Still rejects NULL role.
	if _, err := db.Exec(`insert into users(username,password_hash,created_at) values(?,?,datetime('now'))`, "nullrole", "x"); err == nil {
		t.Fatal("expected NOT NULL violation on role")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := newTestDB(t)
	// Running migrate again on the same DB must not error and must keep the
	// widened constraint.
	if err := db.Migrate("admin", "secret"); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if _, err := db.Exec(`insert into users(username,password_hash,role,created_at) values(?,?,?,datetime('now'))`,
		"op2", "x", RoleOperator); err != nil {
		t.Fatalf("insert after re-migrate: %v", err)
	}
}

func TestAuditAndList(t *testing.T) {
	db := newTestDB(t)
	db.Audit("alice", "container.create", "mudp-alice-dev1")
	db.Audit("bob", "image.pull", "ubuntu:22.04")
	db.Audit("alice", "container.remove", "mudp-alice-dev1")

	all, err := db.AuditList(AuditFilter{})
	if err != nil {
		t.Fatalf("AuditList: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d entries, want 3", len(all))
	}
	// Newest first.
	if all[0].Actor != "alice" || all[0].Action != "container.remove" {
		t.Fatalf("unexpected first entry: %+v", all[0])
	}

	alice, err := db.AuditList(AuditFilter{Actor: "alice"})
	if err != nil {
		t.Fatalf("AuditList alice: %v", err)
	}
	if len(alice) != 2 {
		t.Fatalf("alice got %d, want 2", len(alice))
	}

	pulls, err := db.AuditList(AuditFilter{Action: "image.pull"})
	if err != nil {
		t.Fatalf("AuditList pull: %v", err)
	}
	if len(pulls) != 1 || pulls[0].Actor != "bob" {
		t.Fatalf("pull filter: %+v", pulls)
	}

	target, err := db.AuditList(AuditFilter{Target: "ubuntu"})
	if err != nil {
		t.Fatalf("AuditList target: %v", err)
	}
	if len(target) != 1 {
		t.Fatalf("target filter: %+v", target)
	}
}

func TestUpdateUser(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("carol", "pw1", RoleUser, nil, 5); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("carol", "pw1")
	if err != nil {
		t.Fatal(err)
	}

	// Change password.
	if err := db.UpdateUser(u.ID, "pw2", "", 0, nil); err != nil {
		t.Fatalf("update password: %v", err)
	}
	if _, err := db.Authenticate("carol", "pw1"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := db.Authenticate("carol", "pw2"); err != nil {
		t.Fatalf("new password failed: %v", err)
	}

	// Change role + cap.
	if err := db.UpdateUser(u.ID, "", RoleOperator, 20, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByID(u.ID)
	if got.Role != RoleOperator || got.ContainerCap != 20 {
		t.Fatalf("update role/cap = %q/%d", got.Role, got.ContainerCap)
	}

	// Disable then re-enable.
	dis := true
	if err := db.UpdateUser(u.ID, "", "", 0, &dis); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate("carol", "pw2"); err == nil {
		t.Fatal("disabled user authenticated")
	}
	en := false
	if err := db.UpdateUser(u.ID, "", "", 0, &en); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate("carol", "pw2"); err != nil {
		t.Fatalf("re-enabled user failed: %v", err)
	}
}

func TestCreateStackColumnCount(t *testing.T) {
	// Regression guard: the stacks insert must bind one value per column.
	// (A previous version had 6 placeholders for 7 columns.)
	db := newTestDB(t)
	if err := db.CreateUser("stackowner", "pw", RoleUser, nil, 5); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("stackowner", "pw")
	id, err := db.CreateStack(u.ID, "webapp", "services:\n  web:\n    image: nginx\n", `{"TAG":"1.0"}`, "mudp-stackowner-stack-webapp")
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateStack returned id 0")
	}

	got, err := db.StackByID(id)
	if err != nil {
		t.Fatalf("StackByID: %v", err)
	}
	if got.Name != "webapp" || got.ProjectName != "mudp-stackowner-stack-webapp" || got.EnvJSON != `{"TAG":"1.0"}` {
		t.Errorf("unexpected stack: %+v", got)
	}

	list, err := db.StacksForUser(u.ID, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("StacksForUser owner: %v (len=%d)", err, len(list))
	}

	// Duplicate (owner, name) must error on the unique constraint.
	if _, err := db.CreateStack(u.ID, "webapp", "x", "{}", "dup"); err == nil {
		t.Fatal("expected unique-constraint error for duplicate stack name")
	}

	// Admin sees all stacks; create as another user and verify isolation.
	if err := db.CreateUser("other", "pw", RoleUser, nil, 5); err != nil {
		t.Fatal(err)
	}
	o, _ := db.Authenticate("other", "pw")
	if _, err := db.CreateStack(o.ID, "theirs", "services:\n  x:\n    image: alpine\n", "{}", "mudp-other-stack-theirs"); err != nil {
		t.Fatal(err)
	}
	mine, _ := db.StacksForUser(u.ID, false)
	if len(mine) != 1 {
		t.Errorf("owner should only see their own stacks, got %d", len(mine))
	}
	all, _ := db.StacksForUser(u.ID, true)
	if len(all) != 2 {
		t.Errorf("admin should see all stacks, got %d", len(all))
	}

	// Update + delete.
	if err := db.UpdateStack(id, "updated", "{}"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.StackByID(id)
	if got.ComposeYAML != "updated" {
		t.Errorf("UpdateStack didn't persist: %q", got.ComposeYAML)
	}
	if err := db.DeleteStack(id); err != nil {
		t.Fatal(err)
	}
	list, _ = db.StacksForUser(u.ID, false)
	if len(list) != 0 {
		t.Errorf("stack not deleted, got %d", len(list))
	}
}

func TestRegistriesCRUD(t *testing.T) {
	db := newTestDB(t)
	// Empty to start.
	items, err := db.Registries()
	if err != nil || len(items) != 0 {
		t.Fatalf("expected empty, got %v (err=%v)", items, err)
	}
	if err := db.SaveRegistries([]Registry{
		{ID: 1, Name: "Hub", URL: "docker.io", Username: "u", Token: "tok"},
		{ID: 2, Name: "GHCR", URL: "ghcr.io", Username: "u2", Token: "tok2"},
	}); err != nil {
		t.Fatal(err)
	}
	items, _ = db.Registries()
	if len(items) != 2 || items[0].Token != "tok" {
		t.Errorf("unexpected: %+v", items)
	}
	// Idempotent: saving again replaces, not appends.
	if err := db.SaveRegistries([]Registry{{ID: 1, Name: "Hub", URL: "docker.io", Username: "u", Token: "tok"}}); err != nil {
		t.Fatal(err)
	}
	items, _ = db.Registries()
	if len(items) != 1 {
		t.Errorf("expected 1 after replace, got %d", len(items))
	}
}
