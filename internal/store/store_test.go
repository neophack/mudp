package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mudp/internal/auth"
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

// TestRenameImage covers the re-register path: RenameImage updates the display
// name and docker ref by id while preserving the preset, group links, and source
// ref; it also rejects a name that collides with another row.
func TestRenameImage(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveImage("hash-name", "mudp-hash-name", "ubuntu:22.04"); err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	if err := db.SaveImage("taken", "mudp-taken", "taken:latest"); err != nil {
		t.Fatalf("SaveImage taken: %v", err)
	}

	// Attach a preset + a group link to the row we'll rename, to prove RenameImage
	// keeps them.
	imgs, _ := db.ImagesForUser(1, true)
	var target Image
	for _, im := range imgs {
		if im.DisplayName == "hash-name" {
			target = im
		}
	}
	if target.ID == 0 {
		t.Fatalf("target image not found: %+v", imgs)
	}
	if err := db.CreateGroup("g"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	groups, err := db.Groups()
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	var gid int64
	for _, g := range groups {
		if g.Name == "g" {
			gid = g.ID
		}
	}
	if gid == 0 {
		t.Fatal("group id not resolved")
	}
	if err := db.SetImageGroups(target.ID, []int64{gid}); err != nil {
		t.Fatalf("SetImageGroups: %v", err)
	}
	if err := db.SetImagePreset(target.ID, &ImagePreset{GPUs: "all"}); err != nil {
		t.Fatalf("SetImagePreset: %v", err)
	}

	// Rename: new display name + new docker ref.
	if err := db.RenameImage(target.ID, "asr2pass", "mudp-asr2pass:latest"); err != nil {
		t.Fatalf("RenameImage: %v", err)
	}

	// Row now shows the new identity but keeps its preset/group/source.
	after, err := db.ImageByID(target.ID)
	if err != nil {
		t.Fatalf("ImageByID: %v", err)
	}
	if after.DisplayName != "asr2pass" || after.DockerRef != "mudp-asr2pass:latest" {
		t.Fatalf("rename did not apply: %+v", after)
	}
	if after.SourceRef != "ubuntu:22.04" {
		t.Fatalf("source ref not preserved: %q", after.SourceRef)
	}
	if after.Preset == nil || after.Preset.GPUs != "all" {
		t.Fatalf("preset not preserved: %+v", after.Preset)
	}
	if names := after.Groups; len(names) != 1 || names[0] != "g" {
		t.Fatalf("group link not preserved: %v", names)
	}

	// Renaming to a display name already used by another row must fail (UNIQUE).
	if err := db.RenameImage(target.ID, "taken", "mudp-other:latest"); err == nil {
		t.Fatal("expected UNIQUE violation renaming to a taken name")
	}

	// Renaming a non-existent id is rejected.
	if err := db.RenameImage(999999, "ghost", "mudp-ghost:latest"); err == nil {
		t.Fatal("expected error renaming missing image")
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

	if err := db.CreateUser("visadmin", "x-valid-1234", RoleAdmin, 0, 0, 0); err != nil {
		t.Fatalf("CreateUser visadmin: %v", err)
	}
	if err := db.CreateUser("alice", "x-valid-1234", RoleUser, teamA, 0, 0); err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	if err := db.CreateUser("bob", "x-valid-1234", RoleUser, teamB, 0, 0); err != nil {
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

	// ImageByIDForUser must apply the exact same group-visibility rule as
	// ImagesForUser: bob may resolve the public and team-b images by id, but
	// resolving team-a's id must fail even though it exists in the catalog.
	// This is the guard behind imagePresetResolve (P0-3): without it, any
	// activated user could pass another team's image id and read its preset.
	if _, err := db.ImageByIDForUser(publicID, bob.ID, false); err != nil {
		t.Errorf("bob ImageByIDForUser(public) = %v, want nil error", err)
	}
	if _, err := db.ImageByIDForUser(teamBID, bob.ID, false); err != nil {
		t.Errorf("bob ImageByIDForUser(team-b) = %v, want nil error", err)
	}
	if _, err := db.ImageByIDForUser(teamAID, bob.ID, false); err == nil {
		t.Error("bob ImageByIDForUser(team-a) succeeded; must be forbidden")
	}
	// An admin may resolve any image regardless of group assignment.
	if _, err := db.ImageByIDForUser(teamAID, admin.ID, true); err != nil {
		t.Errorf("admin ImageByIDForUser(team-a) = %v, want nil error", err)
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
	if err := db.CreateUser("carol", "pw1-valid-123", RoleUser, 0, 5, 0); err != nil {
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

// TestUserCapacityLimit verifies that CreateUser and CreateFeishuUser respect
// the configured maximum user count, and that UserCapacityLimit defaults to
// DefaultUserCapacity.
func TestUserCapacityLimit(t *testing.T) {
	db := newTestDB(t)

	if limit, err := db.UserCapacityLimit(); err != nil || limit != DefaultUserCapacity {
		t.Fatalf("UserCapacityLimit default = %d, err=%v, want %d", limit, err, DefaultUserCapacity)
	}
	if err := db.SaveUserCapacityLimit(3); err != nil {
		t.Fatalf("SaveUserCapacityLimit: %v", err)
	}
	if limit, err := db.UserCapacityLimit(); err != nil || limit != 3 {
		t.Fatalf("UserCapacityLimit after save = %d, err=%v, want 3", limit, err)
	}

	// newTestDB already creates an admin user during Migrate, so two more users
	// should fit exactly and a third must be rejected.
	if err := db.CreateUser("one", "pw1-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatalf("create first user: %v", err)
	}
	if err := db.CreateUser("two", "pw2-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if err := db.CreateUser("three", "pw3-valid-123", RoleUser, 0, 5, 0); !errors.Is(err, ErrUserCapacityFull) {
		t.Fatalf("create third user error = %v, want ErrUserCapacityFull", err)
	}

	if _, err := db.CreateFeishuUserWithProfile(auth.FeishuUser{OpenID: "ou-feishu-1", Name: "Feishu One"}); !errors.Is(err, ErrUserCapacityFull) {
		t.Fatalf("create feishu user error = %v, want ErrUserCapacityFull", err)
	}

	count, err := db.UserCount()
	if err != nil || count != 3 {
		t.Fatalf("UserCount = %d, err=%v, want 3", count, err)
	}
}

func TestCreateStackColumnCount(t *testing.T) {
	// Regression guard: the stacks insert must bind one value per column.
	// (A previous version had 6 placeholders for 7 columns.)
	db := newTestDB(t)
	if err := db.CreateUser("stackowner", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.CreateUser("other", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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

// TestMCPTokenExternalKey covers the external key: absent until generated,
// rotatable without disturbing the main token/URL, and enforced by the same
// ownership rule as RotateMCPToken.
func TestMCPTokenExternalKey(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("bob", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	bob, err := db.Authenticate("bob", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}

	cleartext, hash := "tok-ext1", "ext1hash"
	id, err := db.CreateMCPToken(bob.ID, "container-id-2", "dev2", "codex", cleartext, hash, "")
	if err != nil || id == 0 {
		t.Fatalf("CreateMCPToken: %v (id=%d)", err, id)
	}

	// No external key until one is generated.
	got, err := db.MCPTokenByHash(hash)
	if err != nil {
		t.Fatalf("MCPTokenByHash: %v", err)
	}
	if got.ExternalKey != "" || got.ExternalKeyHash != "" {
		t.Fatalf("fresh token should have no external key, got %+v", got)
	}

	// A non-owner cannot generate one.
	if err := db.RotateMCPExternalKey(1, false, id, "ext-tok-1", "ext-hash-1"); err == nil {
		t.Fatal("non-owner should not rotate bob's external key")
	}

	// The owner can, and it does not disturb the main token/hash.
	if err := db.RotateMCPExternalKey(bob.ID, false, id, "ext-tok-1", "ext-hash-1"); err != nil {
		t.Fatalf("RotateMCPExternalKey: %v", err)
	}
	got, err = db.MCPTokenByHash(hash)
	if err != nil {
		t.Fatalf("MCPTokenByHash after rotate: %v", err)
	}
	if got.Token != cleartext {
		t.Errorf("main token changed by external-key rotation: got %q, want %q", got.Token, cleartext)
	}
	if got.ExternalKey != "ext-tok-1" || got.ExternalKeyHash != "ext-hash-1" {
		t.Errorf("external key not stored: %+v", got)
	}

	// Rotating again replaces the old external key outright.
	if err := db.RotateMCPExternalKey(bob.ID, false, id, "ext-tok-2", "ext-hash-2"); err != nil {
		t.Fatalf("second RotateMCPExternalKey: %v", err)
	}
	got, err = db.MCPTokenByHash(hash)
	if err != nil {
		t.Fatalf("MCPTokenByHash after second rotate: %v", err)
	}
	if got.ExternalKey != "ext-tok-2" || got.ExternalKeyHash != "ext-hash-2" {
		t.Errorf("external key not replaced: %+v", got)
	}

	// Admins may rotate any token's external key.
	if err := db.RotateMCPExternalKey(1, true, id, "ext-tok-3", "ext-hash-3"); err != nil {
		t.Fatalf("admin RotateMCPExternalKey: %v", err)
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
	if err := db.CreateUser("bob", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.CreateUser("charlie", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.CreateUser("quotauser", "pw-valid-123", RoleUser, 0, 5, 2*1024*1024*1024); err != nil {
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
	if err := db.CreateUser("dupuser", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	err := db.CreateUser("dupuser", "pw2-valid-123", RoleUser, 0, 5, 0)
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
	if err := db.CreateUser("feishuabc", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.CreateFeishuUserWithProfile(auth.FeishuUser{OpenID: "feishu_abc", Name: "Feishu User"})
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
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, err := db.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.CreateFeishuUserWithProfile(auth.FeishuUser{OpenID: "oid-pp-1", Name: "Feishu Port"})
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
	if err := db.CreateUser("shareowner", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.CreateUser("nogroups", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	u, err := db.Authenticate("nogroups", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}
	if u.Group != DefaultUserGroup {
		t.Fatalf("expected group %s, got %q", DefaultUserGroup, u.Group)
	}
}

// TestDeactivateUser moves an account back to the pending group. The user is
// NOT hard-disabled: they can still authenticate so the UI can show the
// "waiting for admin approval" page. Business endpoints are gated by the
// pending group check, not by the disabled flag.
func TestDeactivateUser(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("active", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if u2.Group != PendingGroup {
		t.Fatalf("expected group %s, got %q", PendingGroup, u2.Group)
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
	if err := db.CreateUser("legacy", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	if err := db.SetUserGroup(u.ID, gid); err != nil {
		t.Fatalf("SetUserGroup: %v", err)
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
	if got.Group != DefaultUserGroup {
		t.Fatalf("expected group %s, got %q", DefaultUserGroup, got.Group)
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
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser("bob", "pw-valid-123", RoleUser, 0, 5, 0); err != nil {
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
	u, err := db.CreateFeishuUserWithProfile(auth.FeishuUser{OpenID: openID, Name: "Test User"})
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
	u, err := db.CreateFeishuUserWithProfile(auth.FeishuUser{OpenID: "ou_zzz999", Name: "Test User"})
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

// TestAccessLogRecordAndQuery verifies the security monitor records and
// retrieves access events, including geographic deduplication for the map.
func TestAccessLogRecordAndQuery(t *testing.T) {
	db := newTestDB(t)

	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, Username: "attacker", IP: "203.0.113.9", FailureReason: "invalid credentials", Country: "China", CountryCode: "CN", City: "Beijing", Latitude: 39.9, Longitude: 116.4, Browser: "Chrome 120", OS: "Windows 10"})
	db.RecordAccess(AccessLog{Event: AccessEventLoginSuccess, Username: "admin", IP: "203.0.113.9", Country: "China", CountryCode: "CN", City: "Beijing", Latitude: 39.9, Longitude: 116.4, Success: true, OS: "Windows 10"})
	db.RecordAccess(AccessLog{Event: AccessEventPageView, IP: "198.51.100.1", Country: "United States", CountryCode: "US", City: "Ashburn", Latitude: 39.04, Longitude: -77.49})
	// A flagged VPN attempt: proxy/hosting set, plus a suspicious marker and
	// client device hints (timezone/screen/etc.) that survive a VPN.
	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, Username: "root", IP: "198.51.100.1", FailureReason: "invalid credentials", Latitude: 39.04, Longitude: -77.49, IsProxy: true, IsHosting: true, ProxyType: "vpn/hosting", Suspicious: "vpn/proxy+tz-mismatch", ClientTimezone: "Asia/Shanghai", ClientScreen: "1920x1080", ClientPlatform: "Win32", ClientCPUCore: 8, ClientMemoryGB: 16})

	// Filtering by event.
	failed, err := db.AccessLogs(AccessLogFilter{Event: AccessEventLoginFailed, Limit: 10})
	if err != nil {
		t.Fatalf("AccessLogs: %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("got %d failed entries, want 2", len(failed))
	}
	// The VPN-flagged entry round-trips the new fields.
	var vpn *AccessLog
	for i := range failed {
		if failed[i].Username == "root" {
			vpn = &failed[i]
		}
	}
	if vpn == nil || !vpn.IsProxy || !vpn.IsHosting || vpn.ProxyType != "vpn/hosting" || vpn.Suspicious == "" || vpn.ClientTimezone != "Asia/Shanghai" || vpn.ClientCPUCore != 8 {
		t.Errorf("VPN/client fields not persisted: %+v", vpn)
	}

	// Free-text search matches username/city.
	byCity, err := db.AccessLogs(AccessLogFilter{Q: "Beijing", Limit: 10})
	if err != nil {
		t.Fatalf("AccessLogs Q: %v", err)
	}
	if len(byCity) != 2 {
		t.Fatalf("got %d Beijing entries, want 2", len(byCity))
	}

	// SuspiciousOnly filter returns only the flagged entry.
	suspicious, err := db.AccessLogs(AccessLogFilter{SuspiciousOnly: true, Limit: 10})
	if err != nil {
		t.Fatalf("AccessLogs suspicious: %v", err)
	}
	if len(suspicious) != 1 {
		t.Fatalf("got %d suspicious entries, want 1", len(suspicious))
	}

	// Geographic points dedupe by ~0.01° buckets: the two Beijing hits collapse
	// to one point with count 2.
	points, err := db.AccessLogGeoPoints(100)
	if err != nil {
		t.Fatalf("AccessLogGeoPoints: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d geo points, want 2 (Beijing, Ashburn)", len(points))
	}
	var beijing *GeoPoint
	for i := range points {
		if points[i].City == "Beijing" {
			beijing = &points[i]
		}
	}
	if beijing == nil || beijing.Count != 2 {
		t.Fatalf("Beijing point count = %v, want 2", beijing)
	}

	// Stats summary.
	stats, err := db.AccessStats()
	if err != nil {
		t.Fatalf("AccessStats: %v", err)
	}
	if stats.TotalVisits != 4 || stats.LoginSuccess != 1 || stats.LoginFailed != 2 || stats.UniqueIPs != 2 {
		t.Errorf("stats = %+v, want totals visits=4 success=1 failed=2 uniqueIPs=2", stats)
	}
	// One IP flagged as VPN/proxy/hosting; one entry carries a suspicious marker.
	if stats.VPNProxy != 1 || stats.Suspicious != 1 {
		t.Errorf("stats VPN/suspicious = %d/%d, want 1/1", stats.VPNProxy, stats.Suspicious)
	}

	// Pruning clears old entries (set a far-future cutoff to delete everything).
	if err := db.PruneAccessLogs(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("PruneAccessLogs: %v", err)
	}
	left, _ := db.AccessLogs(AccessLogFilter{Limit: 100})
	if len(left) != 0 {
		t.Errorf("after prune, %d entries remain, want 0", len(left))
	}
}

// TestAccessStatsTopIPsPrefersPublicIP pins the security dashboard's "Top IPs"
// ranking to the visitor's real WAN address. On an intranet deployment the
// server-seen ip is a private last-hop address shared by every visitor, so
// ranking by it collapses the list to one LAN entry. The browser-reported
// public_ip (WebRTC/STUN) must take precedence when present, falling back to
// ip only when the WAN address is unavailable — mirroring how recordAccess
// derives the geography.
func TestAccessStatsTopIPsPrefersPublicIP(t *testing.T) {
	db := newTestDB(t)

	// Two visitors share the same server-seen private ip (an intranet proxy)
	// but report distinct public IPs. A third visitor has no public_ip, so its
	// private ip is the effective source.
	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, IP: "10.0.0.1", PublicIP: "203.0.113.10", CountryCode: "CN"})
	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, IP: "10.0.0.1", PublicIP: "203.0.113.10", CountryCode: "CN"})
	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, IP: "10.0.0.1", PublicIP: "198.51.100.20", CountryCode: "US"})
	db.RecordAccess(AccessLog{Event: AccessEventLoginFailed, IP: "192.168.1.5", CountryCode: "GB"})

	stats, err := db.AccessStats()
	if err != nil {
		t.Fatalf("AccessStats: %v", err)
	}

	// Ranked by the effective IP (public_ip first, else ip): the shared LAN
	// proxy must NOT top the list — its visitors are split by their real WAN
	// addresses.
	if len(stats.TopIPs) < 3 || stats.TopIPs[0].Label != "203.0.113.10" || stats.TopIPs[0].Count != 2 {
		t.Errorf("TopIPs = %+v, want 203.0.113.10 (count 2) first", stats.TopIPs)
	}
	// The public IPs appear ahead of the private-only fallback.
	var privIdx, pubIdx int
	for i, c := range stats.TopIPs {
		switch c.Label {
		case "192.168.1.5":
			privIdx = i
		case "198.51.100.20":
			pubIdx = i
		}
	}
	if pubIdx > privIdx {
		t.Errorf("public IP 198.51.100.20 (idx %d) must rank above private fallback 192.168.1.5 (idx %d)", pubIdx, privIdx)
	}
	// UniqueIPs counts the server-seen ip only; the public split is a ranking
	// concern, not a counting one, so the two distinct private hops stand.
	if stats.UniqueIPs != 2 {
		t.Errorf("UniqueIPs = %d, want 2 (private hops only)", stats.UniqueIPs)
	}
}

// TestSecuritySettingsRoundTrip verifies the admin security-monitor config is
// persisted and read back, and that an unset DB yields the safe defaults.
func TestSecuritySettingsRoundTrip(t *testing.T) {
	db := newTestDB(t)

	// Unset → defaults (logging on).
	got, err := db.SecuritySettings()
	if err != nil {
		t.Fatalf("SecuritySettings default: %v", err)
	}
	if !got.Enabled || !got.GeoIPLookup || !got.VPNDetect || !got.CollectClient || got.RetentionDays != 90 {
		t.Errorf("defaults = %+v, want logging fully enabled at 90 days", got)
	}

	// Disable GeoIP and shorten retention.
	cfg := got
	cfg.GeoIPLookup = false
	cfg.RetentionDays = 30
	if err := db.SaveSecuritySettings(cfg); err != nil {
		t.Fatalf("SaveSecuritySettings: %v", err)
	}
	again, _ := db.SecuritySettings()
	if again.GeoIPLookup || again.RetentionDays != 30 || !again.Enabled {
		t.Errorf("after save = %+v, want GeoIPLookup=false retention=30 enabled=true", again)
	}

	// A fresh DB (separate file) still returns defaults — the setting is per-DB.
	db2 := newTestDB(t)
	fresh, _ := db2.SecuritySettings()
	if !fresh.GeoIPLookup {
		t.Error("a fresh DB must default to GeoIPLookup=true")
	}
}
