package store

import (
	"database/sql"
	"errors"
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

// TestImagePresetRoundTrip covers the admin preset lifecycle: a preset set via
// SetImagePreset is persisted, surfaced through ImagesForUser, and cleared when
// set to nil. It also confirms the preset_json migration column is added.
func TestImagePresetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveImage("ubuntu", "mudp-ubuntu", "ubuntu:22.04"); err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	imgs, err := db.ImagesForUser(1, true)
	if err != nil {
		t.Fatalf("ImagesForUser: %v", err)
	}
	if len(imgs) != 1 || imgs[0].Preset != nil {
		t.Fatalf("expected one image with no preset, got %+v", imgs)
	}
	imageID := imgs[0].ID

	// Store a preset and read it back through the user-facing query.
	preset := &ImagePreset{
		GPUs:    "all",
		Env:     []string{"VNC_PW=secret"},
		Ports:   []string{"8080"},
		Devices: []string{"/dev/nvidia0"},
	}
	if err := db.SetImagePreset(imageID, preset); err != nil {
		t.Fatalf("SetImagePreset: %v", err)
	}
	imgs, _ = db.ImagesForUser(1, true)
	if len(imgs) != 1 || imgs[0].Preset == nil {
		t.Fatalf("expected preset to be loaded, got %+v", imgs)
	}
	got := imgs[0].Preset
	if got.GPUs != "all" || len(got.Env) != 1 || got.Env[0] != "VNC_PW=secret" ||
		len(got.Ports) != 1 || got.Ports[0] != "8080" ||
		len(got.Devices) != 1 || got.Devices[0] != "/dev/nvidia0" {
		t.Fatalf("preset mismatch: %+v", got)
	}

	// Clearing the preset by passing nil removes it from the image.
	if err := db.SetImagePreset(imageID, nil); err != nil {
		t.Fatalf("SetImagePreset(nil): %v", err)
	}
	imgs, _ = db.ImagesForUser(1, true)
	if len(imgs) != 1 || imgs[0].Preset != nil {
		t.Fatalf("expected preset cleared, got %+v", imgs)
	}

	// Setting a preset on a non-existent image id is rejected.
	if err := db.SetImagePreset(999999, preset); err == nil {
		t.Fatal("expected error setting preset on missing image")
	}
}

// boolPtr returns a pointer to b, used to build preset pointer-typed booleans.
func boolPtr(b bool) *bool { return &b }

// TestValidatePreset guards the server-side preset validation: well-formed presets
// pass, malformed env/ports/devices are rejected before the DB write.
func TestValidatePreset(t *testing.T) {
	good := &ImagePreset{
		GPUs:    "all",
		Env:     []string{"KEY=VALUE", "X=1"},
		Ports:   []string{"8080", "443"},
		Devices: []string{"/dev/nvidia0", "/dev/foo:/dev/bar:rwm"},
	}
	if err := ValidatePreset(good); err != nil {
		t.Fatalf("ValidatePreset(good) = %v", err)
	}
	bad := []struct {
		name   string
		preset *ImagePreset
	}{
		{"bad env", &ImagePreset{Env: []string{"NOEQUALS"}}},
		{"non-numeric port", &ImagePreset{Ports: []string{"http"}}},
		{"bad restart policy", &ImagePreset{RestartPolicy: "forever"}},
		{"empty device", &ImagePreset{Devices: []string{" "}}},
		{"too many colons", &ImagePreset{Devices: []string{"a:b:c:d"}}},
		{"empty cdi", &ImagePreset{CDIDevices: []string{" "}}},
	}
	for _, c := range bad {
		if err := ValidatePreset(c.preset); err == nil {
			t.Errorf("ValidatePreset(%s) expected error, got nil", c.name)
		}
	}
	// nil preset is always valid.
	if err := ValidatePreset(nil); err != nil {
		t.Errorf("ValidatePreset(nil) = %v", err)
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
	if err := db.CreateUser("carol", "pw1", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("carol", "pw1")
	if err != nil {
		t.Fatal(err)
	}

	// Change password.
	if err := db.UpdateUser(u.ID, "pw2", "", 0, nil, nil); err != nil {
		t.Fatalf("update password: %v", err)
	}
	if _, err := db.Authenticate("carol", "pw1"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := db.Authenticate("carol", "pw2"); err != nil {
		t.Fatalf("new password failed: %v", err)
	}

	// Change role + cap.
	if err := db.UpdateUser(u.ID, "", RoleOperator, 20, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByID(u.ID)
	if got.Role != RoleOperator || got.ContainerCap != 20 {
		t.Fatalf("update role/cap = %q/%d", got.Role, got.ContainerCap)
	}

	// Disable then re-enable.
	dis := true
	if err := db.UpdateUser(u.ID, "", "", 0, nil, &dis); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate("carol", "pw2"); err == nil {
		t.Fatal("disabled user authenticated")
	}
	en := false
	if err := db.UpdateUser(u.ID, "", "", 0, nil, &en); err != nil {
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
	if err := db.CreateUser("stackowner", "pw", RoleUser, nil, 5, 0); err != nil {
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
	if err := db.CreateUser("other", "pw", RoleUser, nil, 5, 0); err != nil {
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

// TestMCPTokenCRUD covers the full token lifecycle: create, look up by hash,
// list, delete, and ownership isolation between users.
func TestMCPTokenCRUD(t *testing.T) {
	db := newTestDB(t)
	// admin (id=1) exists from Migrate; create a second user to test isolation.
	if err := db.CreateUser("alice", "pw", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, err := db.Authenticate("alice", "pw")
	if err != nil {
		t.Fatal(err)
	}

	// Create a token for alice on a (mock) container.
	cleartext := "tok-abc123"
	hash := "abc123hash"
	id, err := db.CreateMCPToken(alice.ID, "container-id-1", "dev", "claude-code", cleartext, hash, "")
	if err != nil || id == 0 {
		t.Fatalf("CreateMCPToken: %v (id=%d)", err, id)
	}

	// Look it up by hash.
	got, err := db.MCPTokenByHash(hash)
	if err != nil {
		t.Fatalf("MCPTokenByHash: %v", err)
	}
	if got.ID != id || got.ContainerID != "container-id-1" || got.Label != "claude-code" {
		t.Fatalf("token mismatch: %+v", got)
	}
	if got.Expired() {
		t.Fatal("non-expiring token reports expired")
	}
	if got.Token != cleartext {
		t.Errorf("token cleartext not persisted: got %q, want %q", got.Token, cleartext)
	}

	// Alice sees her token; the list includes owner.
	mine, err := db.MCPTokensForUser(alice.ID, false)
	if err != nil || len(mine) != 1 {
		t.Fatalf("MCPTokensForUser alice: %v (len=%d)", err, len(mine))
	}
	if mine[0].Owner != "alice" {
		t.Errorf("owner not joined: %q", mine[0].Owner)
	}

	// Admin sees all tokens (1 so far).
	all, err := db.MCPTokensForUser(alice.ID, true)
	if err != nil || len(all) != 1 {
		t.Fatalf("admin list: %v (len=%d)", err, len(all))
	}

	// Filter by container.
	byContainer, err := db.MCPTokensForContainer(alice.ID, false, "container-id-1")
	if err != nil || len(byContainer) != 1 {
		t.Fatalf("byContainer: %v (len=%d)", err, len(byContainer))
	}
	other, err := db.MCPTokensForContainer(alice.ID, false, "does-not-exist")
	if err != nil || len(other) != 0 {
		t.Fatalf("unknown container should have 0 tokens: %v (len=%d)", err, len(other))
	}

	// Touch last_used_at (best-effort, should not error).
	if err := db.MCPTokenTouch(id); err != nil {
		t.Fatalf("MCPTokenTouch: %v", err)
	}

	// A second user (the admin, id=1) cannot delete alice's token.
	if err := db.DeleteMCPToken(1, false, id); err == nil {
		t.Fatal("non-owner should not delete alice's token")
	}
	// But alice can.
	if err := db.DeleteMCPToken(alice.ID, false, id); err != nil {
		t.Fatalf("DeleteMCPToken: %v", err)
	}
	// Gone.
	if _, err := db.MCPTokenByHash(hash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("token should be gone, got err=%v", err)
	}
}

// TestMCPTokenExpiry verifies Expired() and the expiry-stamp path.
func TestMCPTokenExpiry(t *testing.T) {
	db := newTestDB(t)
	// A token that already expired.
	past := "2000-01-01T00:00:00Z"
	id, err := db.CreateMCPToken(1, "c1", "dev", "bot", "expired-tok", "expired-hash", past)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.MCPTokenByHash("expired-hash")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || !got.Expired() {
		t.Fatalf("token should be expired: %+v", got)
	}

	// A token that expires in the future.
	future := "2099-01-01T00:00:00Z"
	if _, err := db.CreateMCPToken(1, "c2", "dev2", "bot2", "future-tok", "future-hash", future); err != nil {
		t.Fatal(err)
	}
	got2, err := db.MCPTokenByHash("future-hash")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Expired() {
		t.Fatal("future token reports expired")
	}
}



// TestMigrationPreservesPortPrefix simulates an older DB with the restrictive
// role CHECK and a non-zero port_prefix, then runs Migrate and asserts the
// prefix survives the table rebuild.
func TestMigrationPreservesPortPrefix(t *testing.T) {
	db := newTestDB(t)
	// Set a distinctive port prefix on the admin user created by Migrate.
	users, err := db.Users()
	if err != nil || len(users) != 1 {
		t.Fatalf("expected one admin user, got %+v err=%v", users, err)
	}
	admin := users[0]
	if err := db.UpdateUserPortPrefix(admin.ID, 150); err != nil {
		t.Fatalf("UpdateUserPortPrefix: %v", err)
	}

	// Force the old restrictive CHECK by recreating the table as it once was.
	steps := []string{
		`alter table users rename to users_old`,
		`create table users (
			id integer primary key autoincrement,
			username text not null unique,
			password_hash text not null,
			role text not null check(role in ('admin','user')),
			disabled integer not null default 0,
			container_cap integer not null default 10,
			port_prefix integer not null default 0,
			created_at text not null,
			last_login_at text,
			feishu_open_id text default '',
			comment text default ''
		)`,
		`insert into users(id, username, password_hash, role, disabled, container_cap, port_prefix, created_at, last_login_at, feishu_open_id, comment)
		 select id, username, password_hash, role, disabled, container_cap, port_prefix, created_at, last_login_at, feishu_open_id, comment from users_old`,
		`drop table users_old`,
	}
	for _, s := range steps {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("recreate old schema: %v", err)
		}
	}

	// Pretend the widen-role migration has not run so Migrate re-applies it.
	if _, err := db.Exec(`delete from schema_version where version >= 10`); err != nil {
		t.Fatalf("reset schema version: %v", err)
	}
	// Running migrate again must preserve the port_prefix.
	if err := db.Migrate("admin", "secret"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	u, err := db.UserByID(admin.ID)
	if err != nil {
		t.Fatalf("UserByID after migrate: %v", err)
	}
	if u.PortPrefix != 150 {
		t.Fatalf("port_prefix = %d, want 150", u.PortPrefix)
	}
	// And the widened constraint must allow operators again.
	if _, err := db.Exec(`insert into users(username,password_hash,role,created_at) values(?,?,?,datetime('now'))`, "op", "x", RoleOperator); err != nil {
		t.Fatalf("insert operator after migration: %v", err)
	}
}

// TestSchemaVersionTracksMigrations confirms the schema_version table records
// every applied migration.
func TestSchemaVersionTracksMigrations(t *testing.T) {
	db := newTestDB(t)
	var version int
	err := db.QueryRow(`select version from schema_version order by version desc limit 1`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}


// TestCreateUserQuota ensures the netdisk quota is persisted and returned.
func TestCreateUserQuota(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("quotauser", "pw", RoleUser, nil, 5, 2*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("quotauser", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if u.NetdiskQuotaBytes != 2*1024*1024*1024 {
		t.Fatalf("NetdiskQuotaBytes = %d, want %d", u.NetdiskQuotaBytes, 2*1024*1024*1024)
	}
}

// TestNetdiskSharePassword ensures password-protected shares store and expose
// the has-password flag without leaking the hash.
func TestNetdiskSharePassword(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("shareowner", "pw", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("shareowner", "pw")
	if err := db.CreateNetdiskShare(u.ID, "tok1", "files", []string{"a.txt"}, "", true, "hashed-password"); err != nil {
		t.Fatal(err)
	}
	s, err := db.NetdiskShare("tok1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasPassword {
		t.Error("HasPassword = false for password-protected share")
	}
	if s.PasswordHash != "hashed-password" {
		t.Errorf("PasswordHash = %q, want %q", s.PasswordHash, "hashed-password")
	}
}
