package store

import (
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
		SSH:     boolPtr(true),
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
		len(got.Ports) != 1 || got.Ports[0] != "8080" || got.SSH == nil || !*got.SSH ||
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

func TestFusedLayerCRUD(t *testing.T) {
	db := newTestDB(t)

	layer := FusedLayer{
		CacheKey:    "ssh-key-1",
		BaseRef:     "ubuntu:22.04",
		BaseImageID: "sha256:abc",
		LayerRef:    "mudp-layer-ssh-ubuntu-abc:latest",
		Service:     "ssh",
		ScriptHash:  "hash-ssh",
	}
	if err := db.SaveFusedLayer(layer); err != nil {
		t.Fatalf("SaveFusedLayer: %v", err)
	}

	got, ok, err := db.GetFusedLayer(layer.CacheKey)
	if err != nil {
		t.Fatalf("GetFusedLayer: %v", err)
	}
	if !ok {
		t.Fatal("GetFusedLayer returned ok=false after save")
	}
	if got.LayerRef != layer.LayerRef || got.Service != layer.Service {
		t.Fatalf("GetFusedLayer returned unexpected layer: %+v", got)
	}

	layers, err := db.ListFusedLayers()
	if err != nil {
		t.Fatalf("ListFusedLayers: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}

	// Upsert: save again with a different layer_ref should update.
	layer.LayerRef = "mudp-layer-ssh-ubuntu-xyz:latest"
	if err := db.SaveFusedLayer(layer); err != nil {
		t.Fatalf("SaveFusedLayer upsert: %v", err)
	}
	got, ok, _ = db.GetFusedLayer(layer.CacheKey)
	if !ok || got.LayerRef != layer.LayerRef {
		t.Fatalf("upsert did not update layer_ref: %+v", got)
	}

	if err := db.DeleteFusedLayer(layer.CacheKey); err != nil {
		t.Fatalf("DeleteFusedLayer: %v", err)
	}
	_, ok, _ = db.GetFusedLayer(layer.CacheKey)
	if ok {
		t.Fatal("layer still exists after delete")
	}
}

// TestDefaultScriptsAreUserAware verifies the default SSH/VSCode bootstrap
// scripts target $MUDP_CONNECTION_USER / $MUDP_HOME instead of a hardcoded root
// account, so non-root images (USER node, USER 1000) can build SSH/VSCode.
func TestDefaultScriptsAreUserAware(t *testing.T) {
	db := newTestDB(t)
	cfg, err := db.ScriptSettings()
	if err != nil {
		t.Fatalf("ScriptSettings: %v", err)
	}
	for _, want := range []string{
		"MUDP_CONNECTION_USER",
		"MUDP_HOME",
		"${MUDP_CONNECTION_USER}:${MUDP_ACCESS_PASSWORD}",
	} {
		if !strings.Contains(cfg.SSHScript, want) {
			t.Errorf("default SSH script missing %q", want)
		}
	}
	for _, want := range []string{
		"MUDP_CONNECTION_USER",
		"MUDP_HOME",
		"$MUDP_HOME/.config/code-server",
		"gosu",
	} {
		if !strings.Contains(cfg.VSCodeScript, want) {
			t.Errorf("default VS Code script missing %q", want)
		}
	}
	// The old hardcoded root-only password line and /root code-server path must
	// be gone from the defaults.
	if strings.Contains(cfg.SSHScript, "root:${MUDP_ACCESS_PASSWORD}") {
		t.Errorf("default SSH script still hardcodes root-only chpasswd")
	}
	if strings.Contains(cfg.VSCodeScript, "/root/.config/code-server/config.yaml") {
		t.Errorf("default VS Code script still writes config under /root")
	}
}

// TestUpgradeDefaultScriptsReplacesRootHardcodedScripts ensures a stored script
// matching the legacy root-hardcoded default is upgraded to the user-aware
// version automatically (existing deployments get the non-root fix).
func TestUpgradeDefaultScriptsReplacesRootHardcodedScripts(t *testing.T) {
	db := newTestDB(t)
	// Plant the previous-generation root-hardcoded SSH script body. Note: do
	// not write the literal "MUDP_BUILD_PHASE" anywhere (even in a comment) or
	// the marker check will treat it as already-upgraded.
	legacy := "#!/bin/sh\nset -eu\n" +
		"have_cmd() { command -v \"$1\" >/dev/null 2>&1; }\n" +
		"install_packages() { apt-get install -y openssh-server; }\n" +
		"have_cmd sshd || install_packages\n" +
		"cat <<EOF | chpasswd\nroot:${MUDP_ACCESS_PASSWORD}\nEOF\n"
	if err := db.SaveScriptSettings(ScriptSettings{
		SSHScript:    legacy,
		VSCodeScript: defaultVSCodeScript(),
	}); err != nil {
		t.Fatalf("SaveScriptSettings: %v", err)
	}
	if err := db.upgradeDefaultScripts(); err != nil {
		t.Fatalf("upgradeDefaultScripts: %v", err)
	}
	cfg, err := db.ScriptSettings()
	if err != nil {
		t.Fatalf("ScriptSettings: %v", err)
	}
	if strings.Contains(cfg.SSHScript, "root:${MUDP_ACCESS_PASSWORD}") {
		t.Errorf("legacy root-hardcoded SSH script was not upgraded")
	}
	if !strings.Contains(cfg.SSHScript, "MUDP_CONNECTION_USER") {
		t.Errorf("upgraded SSH script is not user-aware")
	}
}
