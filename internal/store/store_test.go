package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
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
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
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

// TestImagesForUserVisibility verifies that unassigned images are visible to
// every activated user, while images assigned to groups are only visible to
// members of those groups.
func TestImagesForUserVisibility(t *testing.T) {
	db := newTestDB(t)

	if err := db.CreateGroup("team-a"); err != nil {
		t.Fatalf("CreateGroup team-a: %v", err)
	}
	if err := db.CreateGroup("team-b"); err != nil {
		t.Fatalf("CreateGroup team-b: %v", err)
	}
	groups, err := db.Groups()
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	var teamA, teamB int64
	for _, g := range groups {
		if g.Name == "team-a" {
			teamA = g.ID
		}
		if g.Name == "team-b" {
			teamB = g.ID
		}
	}
	if teamA == 0 || teamB == 0 {
		t.Fatal("failed to resolve group ids")
	}

	if err := db.CreateUser("visadmin", "x-valid-1234", RoleAdmin, nil, 0, 0); err != nil {
		t.Fatalf("CreateUser visadmin: %v", err)
	}
	if err := db.CreateUser("alice", "x-valid-1234", RoleUser, []int64{teamA}, 0, 0); err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	if err := db.CreateUser("bob", "x-valid-1234", RoleUser, []int64{teamB}, 0, 0); err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}

	admin, _ := db.UserByUsername("visadmin")
	alice, _ := db.UserByUsername("alice")
	bob, _ := db.UserByUsername("bob")

	if err := db.SaveImage("public-img", "mudp-public", "public:latest"); err != nil {
		t.Fatalf("SaveImage public: %v", err)
	}
	if err := db.SaveImage("team-a-img", "mudp-a", "a:latest"); err != nil {
		t.Fatalf("SaveImage team-a: %v", err)
	}
	if err := db.SaveImage("team-b-img", "mudp-b", "b:latest"); err != nil {
		t.Fatalf("SaveImage team-b: %v", err)
	}

	imgs, _ := db.ImagesForUser(admin.ID, true)
	var publicID, teamAID, teamBID int64
	for _, img := range imgs {
		switch img.DisplayName {
		case "public-img":
			publicID = img.ID
		case "team-a-img":
			teamAID = img.ID
		case "team-b-img":
			teamBID = img.ID
		}
	}
	if publicID == 0 || teamAID == 0 || teamBID == 0 {
		t.Fatalf("failed to resolve image ids: %+v", imgs)
	}

	// public-img remains unassigned; team images are restricted.
	if err := db.SetImageGroups(teamAID, []int64{teamA}); err != nil {
		t.Fatalf("SetImageGroups team-a: %v", err)
	}
	if err := db.SetImageGroups(teamBID, []int64{teamB}); err != nil {
		t.Fatalf("SetImageGroups team-b: %v", err)
	}

	names := func(userID int64) map[string]bool {
		imgs, err := db.ImagesForUser(userID, false)
		if err != nil {
			t.Fatalf("ImagesForUser(%d): %v", userID, err)
		}
		out := make(map[string]bool, len(imgs))
		for _, img := range imgs {
			out[img.DisplayName] = true
		}
		return out
	}

	aliceImgs := names(alice.ID)
	if !aliceImgs["public-img"] || !aliceImgs["team-a-img"] || aliceImgs["team-b-img"] {
		t.Fatalf("alice visibility wrong: %v", aliceImgs)
	}

	bobImgs := names(bob.ID)
	if !bobImgs["public-img"] || bobImgs["team-a-img"] || !bobImgs["team-b-img"] {
		t.Fatalf("bob visibility wrong: %v", bobImgs)
	}
}

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
	if err := db.CreateUser("carol", "pw1-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("carol", "pw1-valid-123")
	if err != nil {
		t.Fatal(err)
	}

	// Change password.
	if err := db.UpdateUser(u.ID, "pw2-valid-123", "", 0, nil, nil); err != nil {
		t.Fatalf("update password: %v", err)
	}
	if _, err := db.Authenticate("carol", "pw1-valid-123"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := db.Authenticate("carol", "pw2-valid-123"); err != nil {
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
	if _, err := db.Authenticate("carol", "pw2-valid-123"); err == nil {
		t.Fatal("disabled user authenticated")
	}
	en := false
	if err := db.UpdateUser(u.ID, "", "", 0, nil, &en); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate("carol", "pw2-valid-123"); err != nil {
		t.Fatalf("re-enabled user failed: %v", err)
	}
}

func TestCreateStackColumnCount(t *testing.T) {
	// Regression guard: the stacks insert must bind one value per column.
	// (A previous version had 6 placeholders for 7 columns.)
	db := newTestDB(t)
	if err := db.CreateUser("stackowner", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("stackowner", "pw-valid-123")
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
	if err := db.CreateUser("other", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	o, _ := db.Authenticate("other", "pw-valid-123")
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
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, err := db.Authenticate("alice", "pw-valid-123")
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

// TestMCPTokenListOrphanedOwner is a regression test: legacy databases can hold
// tokens whose owner was deleted without cascading (foreign_keys was only set
// on one pooled connection). Listing must tolerate the NULL owner, and the
// cleanup migration must remove the orphans.
func TestMCPTokenListOrphanedOwner(t *testing.T) {
	db := newTestDB(t)
	// Pin the pool to one connection so the pragma below applies to every
	// following statement (SQLite pragmas are per-connection).
	db.SetMaxOpenConns(1)
	if err := db.CreateUser("bob", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	bob, err := db.Authenticate("bob", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMCPToken(bob.ID, "container-id-9", "dev", "claude", "tok-orphan", "orphan-hash", ""); err != nil {
		t.Fatal(err)
	}
	// Recreate a legacy orphan: with foreign_keys off, deleting the user does
	// not cascade to mcp_tokens.
	if _, err := db.Exec(`pragma foreign_keys = off`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma foreign_keys = on`); err != nil {
		t.Fatal(err)
	}

	all, err := db.MCPTokensForUser(1, true)
	if err != nil {
		t.Fatalf("admin list with orphaned token: %v", err)
	}
	if len(all) != 1 || all[0].Owner != "" {
		t.Fatalf("expected one token with empty owner, got %+v", all)
	}
	byContainer, err := db.MCPTokensForContainer(1, true, "container-id-9")
	if err != nil || len(byContainer) != 1 {
		t.Fatalf("byContainer with orphaned token: %v (len=%d)", err, len(byContainer))
	}

	// The cleanup migration deletes orphaned tokens.
	if err := migrateDeleteOrphanedMCPTokens(db); err != nil {
		t.Fatal(err)
	}
	left, err := db.MCPTokensForUser(1, true)
	if err != nil || len(left) != 0 {
		t.Fatalf("orphan cleanup: %v (len=%d)", err, len(left))
	}

	// With foreign_keys enabled (via the DSN), deleting a user cascades.
	if err := db.CreateUser("charlie", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	charlie, err := db.Authenticate("charlie", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMCPToken(charlie.ID, "c2", "dev2", "bot", "tok-casc", "casc-hash", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(charlie.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MCPTokenByHash("casc-hash"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("token should cascade-delete with its owner, got err=%v", err)
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
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
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
	if err := db.CreateUser("quotauser", "pw-valid-123", RoleUser, nil, 5, 2*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("quotauser", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}
	if u.NetdiskQuotaBytes != 2*1024*1024*1024 {
		t.Fatalf("NetdiskQuotaBytes = %d, want %d", u.NetdiskQuotaBytes, 2*1024*1024*1024)
	}
}

// TestCreateUserDuplicateUsername verifies that a duplicate username returns
// the underlying SQLite error immediately rather than being swallowed by the
// port-prefix retry loop.
func TestCreateUserDuplicateUsername(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("dupuser", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	err := db.CreateUser("dupuser", "pw2-valid-123", RoleUser, nil, 5, 0)
	if err == nil {
		t.Fatal("expected error for duplicate username")
	}
	if strings.Contains(err.Error(), "could not allocate a unique port prefix") {
		t.Fatalf("duplicate username masked as port-prefix error: %v", err)
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("expected UNIQUE constraint error, got: %v", err)
	}
}

// TestCreateFeishuUserRetriesUsername verifies that CreateFeishuUser retries
// with a numbered suffix when the generated username is already taken.
func TestCreateFeishuUserRetriesUsername(t *testing.T) {
	db := newTestDB(t)
	// Pre-create a local user whose username matches the Feishu-generated one.
	if err := db.CreateUser("feishuabc", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.CreateFeishuUser("oid-123", "feishuabc", "Feishu User")
	if err != nil {
		t.Fatalf("CreateFeishuUser: %v", err)
	}
	if u.Username != "feishuabc-1" {
		t.Fatalf("expected username feishuabc-1, got %q", u.Username)
	}
}

// TestCreateFeishuUserAssignsNextPortPrefix verifies that Feishu-created users
// use the same incremental port-prefix allocator as local users.
func TestCreateFeishuUserAssignsNextPortPrefix(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, err := db.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.CreateFeishuUser("oid-pp-1", "feishuport", "Feishu Port")
	if err != nil {
		t.Fatalf("CreateFeishuUser: %v", err)
	}
	if u.PortPrefix != alice.PortPrefix+1 {
		t.Fatalf("portPrefix = %d, want %d", u.PortPrefix, alice.PortPrefix+1)
	}
}

// TestNetdiskSharePassword ensures password-protected shares store and expose
// the has-password flag without leaking the hash.
func TestNetdiskSharePassword(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("shareowner", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("shareowner", "pw-valid-123")
	if err := db.CreateNetdiskShare(u.ID, "tok1", "files", []string{"a.txt"}, "", true, "hashed-password", "secret"); err != nil {
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
	// The public single-fetch path must not expose the cleartext code...
	if s.Password != "" {
		t.Errorf("Password = %q on single fetch, want empty", s.Password)
	}
	// ...while the owner/admin list queries expose it for later viewing.
	items, err := db.NetdiskShares(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Password != "secret" {
		t.Errorf("NetdiskShares Password = %q, want %q", items[0].Password, "secret")
	}
}

// TestDefaultGroupsExist confirms that Migrate creates both the pending and
// default users groups.
func TestDefaultGroupsExist(t *testing.T) {
	db := newTestDB(t)
	groups, err := db.Groups()
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	hasPending, hasUsers := false, false
	for _, g := range groups {
		if g.Name == PendingGroup {
			hasPending = true
		}
		if g.Name == DefaultUserGroup {
			hasUsers = true
		}
	}
	if !hasPending {
		t.Error("missing pending group after migration")
	}
	if !hasUsers {
		t.Error("missing default users group after migration")
	}
}

// TestCreateUserAssignsDefaultGroup verifies that a local user created without
// explicit groups is automatically placed in the default users group.
func TestCreateUserAssignsDefaultGroup(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("nogroups", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("nogroups", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Groups) != 1 || u.Groups[0] != DefaultUserGroup {
		t.Fatalf("expected groups [%s], got %v", DefaultUserGroup, u.Groups)
	}
}

// TestDeactivateUser moves an account back to the pending group. The user is
// NOT hard-disabled: they can still authenticate so the UI can show the
// "waiting for admin approval" page. Business endpoints are gated by the
// pending group check, not by the disabled flag.
func TestDeactivateUser(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("active", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("active", "pw-valid-123")
	if err := db.DeactivateUser(u.ID); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}
	u2, err := db.UserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u2.Disabled {
		t.Error("deactivate must not set the disabled flag; use UpdateUser for a hard lockout")
	}
	if len(u2.Groups) != 1 || u2.Groups[0] != PendingGroup {
		t.Fatalf("expected groups [%s], got %v", PendingGroup, u2.Groups)
	}
	// The user can still log in — the pending waiting page requires a session.
	if _, err := db.Authenticate("active", "pw-valid-123"); err != nil {
		t.Fatalf("deactivated user should still authenticate, got: %v", err)
	}
}

// TestApproveClearsDisabled verifies that approval clears a stale disabled
// flag, so users deactivated under the old (disabled-coupled) semantics are
// fully restored once an admin approves them.
func TestApproveClearsDisabled(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("legacy", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Authenticate("legacy", "pw-valid-123")
	// Simulate historical data: disabled=1 while still in the pending group.
	if err := db.DeactivateUser(u.ID); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}
	hardDisable := true
	if err := db.UpdateUser(u.ID, "", "", 0, nil, &hardDisable); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	// Mirror the server-side approve flow: move to the default group, then
	// clear the disabled flag.
	gid, err := db.DefaultUserGroupID()
	if err != nil {
		t.Fatalf("DefaultUserGroupID: %v", err)
	}
	if err := db.SetUserGroups(u.ID, []int64{gid}); err != nil {
		t.Fatalf("SetUserGroups: %v", err)
	}
	disabledOff := false
	if err := db.UpdateUser(u.ID, "", "", 0, nil, &disabledOff); err != nil {
		t.Fatalf("UpdateUser clear: %v", err)
	}
	got, err := db.UserByID(u.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.Disabled {
		t.Error("approve must clear the disabled flag")
	}
	if len(got.Groups) != 1 || got.Groups[0] != DefaultUserGroup {
		t.Fatalf("expected groups [%s], got %v", DefaultUserGroup, got.Groups)
	}
	// And the user must now be able to authenticate.
	if _, err := db.Authenticate("legacy", "pw-valid-123"); err != nil {
		t.Fatalf("approved user should authenticate, got: %v", err)
	}
}

// TestNotificationLifecycle covers creating, listing, marking read, and
// per-user isolation.
func TestNotificationLifecycle(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser("bob", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, _ := db.Authenticate("alice", "pw-valid-123")
	bob, _ := db.Authenticate("bob", "pw-valid-123")

	if err := db.NotifyUser(alice.ID, Notification{
		Type:    NotificationUserApproved,
		Title:   "Approved",
		Message: "You have been approved.",
	}); err != nil {
		t.Fatalf("NotifyUser: %v", err)
	}
	if err := db.NotifyUser(bob.ID, Notification{
		Type:    NotificationSystemAlert,
		Title:   "Alert",
		Message: "System issue.",
	}); err != nil {
		t.Fatalf("NotifyUser bob: %v", err)
	}

	aliceItems, unread, err := db.NotificationsForUser(alice.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceItems) != 1 || unread != 1 {
		t.Fatalf("alice notifications = %d unread=%d", len(aliceItems), unread)
	}
	if aliceItems[0].Type != NotificationUserApproved {
		t.Fatalf("unexpected type %q", aliceItems[0].Type)
	}

	bobItems, bobUnread, err := db.NotificationsForUser(bob.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobItems) != 1 || bobUnread != 1 {
		t.Fatalf("bob notifications = %d unread=%d", len(bobItems), bobUnread)
	}

	if err := db.MarkNotificationsRead(alice.ID, []int64{aliceItems[0].ID}); err != nil {
		t.Fatal(err)
	}
	_, unread, _ = db.NotificationsForUser(alice.ID, 10)
	if unread != 0 {
		t.Fatalf("expected 0 unread after mark, got %d", unread)
	}
}

// TestSetupWizardLeavesNoAdmin confirms that Migrate does not create an admin
// when the provided password is empty, allowing the setup wizard to run.
func TestSetupWizardLeavesNoAdmin(t *testing.T) {
	// Migrate a fresh temp DB with an empty admin password.
	// and migrate with an empty password.
	path := filepath.Join(t.TempDir(), "setup.db")
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db2.Close()
	if err := db2.Migrate("admin", ""); err != nil {
		t.Fatalf("Migrate with empty password: %v", err)
	}
	var n int
	if err := db2.QueryRow(`select count(*) from users where role='admin'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no admin, got %d", n)
	}
	groups, _ := db2.Groups()
	hasUsers := false
	for _, g := range groups {
		if g.Name == DefaultUserGroup {
			hasUsers = true
		}
	}
	if !hasUsers {
		t.Error("expected default users group to exist even without admin")
	}
}

// TestFeishuUserCannotPasswordLogin covers the account-takeover fix: an SSO
// account's password must not be derivable from its identifiers. The username
// is the open ID with separators stripped, so hashing the open ID as the
// password made every Feishu account reachable through the normal login form.
func TestFeishuUserCannotPasswordLogin(t *testing.T) {
	db := newTestDB(t)
	const openID = "ou_a1b2c3d4"
	u, err := db.CreateFeishuUser(openID, "oua1b2c3d4", "Test User")
	if err != nil {
		t.Fatalf("CreateFeishuUser: %v", err)
	}
	for _, guess := range []string{openID, "oua1b2c3d4", "", "!"} {
		if _, err := db.Authenticate(u.Username, guess); err == nil {
			t.Errorf("password login succeeded with %q; SSO accounts must have no password", guess)
		}
	}
	// The OAuth path itself still resolves the account.
	if got, err := db.UserByFeishu(openID); err != nil || got.ID != u.ID {
		t.Errorf("UserByFeishu = %v, %v; want the created user", got, err)
	}
}

// An administrator can still hand an SSO user a real password, and it works.
func TestFeishuUserWithAdminSetPasswordCanLogin(t *testing.T) {
	db := newTestDB(t)
	u, err := db.CreateFeishuUser("ou_zzz999", "ouzzz999", "Test User")
	if err != nil {
		t.Fatalf("CreateFeishuUser: %v", err)
	}
	if err := db.UpdateUser(u.ID, "a-real-password", "", 0, nil, nil); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if _, err := db.Authenticate(u.Username, "a-real-password"); err != nil {
		t.Errorf("admin-set password should authenticate: %v", err)
	}
	if _, err := db.Authenticate(u.Username, "ou_zzz999"); err == nil {
		t.Error("open ID must not authenticate")
	}
}
