package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestPruneableLogTablesIsAllowList confirms the prune allow-list contains the
// log tables an admin may clear and, crucially, omits every user/system table.
// This is the safety boundary the whole feature rests on, so it is pinned here.
func TestPruneableLogTablesIsAllowList(t *testing.T) {
	for _, name := range PruneableLogTables() {
		if !IsPruneableLogTable(name) {
			t.Errorf("PruneableLogTables lists %q but IsPruneableLogTable disagrees", name)
		}
	}
	must := map[string]bool{
		"audit_logs": true, "access_logs": true,
		"mcp_usage_logs": true, "mcp_attack_logs": true,
		"resource_samples": true,
	}
	for name := range must {
		if !IsPruneableLogTable(name) {
			t.Errorf("expected %q to be pruneable", name)
		}
	}
	// User/system tables must never be pruneable.
	for _, name := range []string{"users", "groups", "images", "settings", "port_forwards", "schema_version"} {
		if IsPruneableLogTable(name) {
			t.Errorf("user/system table %q must not be pruneable", name)
		}
	}
}

// TestPruneLogsRejectsUserTables asserts the central safety property: a request
// to prune a user table is refused outright, before any row is touched. This is
// what stops a malformed or hostile request from deleting accounts.
func TestPruneLogsRejectsUserTables(t *testing.T) {
	db := newTestDB(t)
	_, err := db.PruneLogs([]string{"users"}, time.Now())
	if err == nil {
		t.Fatal("PruneLogs(users) succeeded; user tables must be refused")
	}
	// A mix with one safe and one unsafe name is still refused wholesale.
	_, err = db.PruneLogs([]string{"audit_logs", "users"}, time.Now())
	if err == nil {
		t.Fatal("PruneLogs([audit_logs, users]) succeeded; the unsafe name must abort the call")
	}
}

// TestPruneLogsDeletesByCutoff inserts audit rows at two timestamps and checks
// only the older half is removed for a given cutoff, and everything is removed
// for a far-future cutoff. This is the behaviour the handler relies on for both
// "older than N days" and "clear all".
func TestPruneLogsDeletesByCutoff(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -60)
	for i := 0; i < 5; i++ {
		db.Exec(`insert into audit_logs(actor, action, target, created_at) values(?,?,?,?)`,
			"old", "act", "t", old.Format(time.RFC3339))
	}
	for i := 0; i < 3; i++ {
		db.Exec(`insert into audit_logs(actor, action, target, created_at) values(?,?,?,?)`,
			"new", "act", "t", now.Format(time.RFC3339))
	}

	// Cutoff 30 days ago: the 5 "old" rows (60 days) go, the 3 "new" stay.
	deleted, err := db.PruneLogs([]string{"audit_logs"}, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneLogs: %v", err)
	}
	if got := deleted["audit_logs"]; got != 5 {
		t.Fatalf("deleted[audit_logs] = %d, want 5", got)
	}
	var remaining int
	if err := db.QueryRow(`select count(*) from audit_logs`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 3 {
		t.Fatalf("remaining audit rows = %d, want 3", remaining)
	}

	// Far-future cutoff: clears the rest. The result map reports the 3 removed.
	deleted, err = db.PruneLogs([]string{"audit_logs"}, now.AddDate(100, 0, 0))
	if err != nil {
		t.Fatalf("PruneLogs(all): %v", err)
	}
	if got := deleted["audit_logs"]; got != 3 {
		t.Fatalf("deleted[audit_logs] (all) = %d, want 3", got)
	}
	if err := db.QueryRow(`select count(*) from audit_logs`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining audit rows = %d, want 0 after clearing all", remaining)
	}
}

// TestDBUsageReportsKnownTables checks the usage report lists the log tables
// and the user tables (for visibility), carries a non-zero main-file size, and
// reports the free-page count without error.
func TestDBUsageReportsKnownTables(t *testing.T) {
	// Open in a known path so the on-disk sizes are deterministic and the file
	// actually exists for os.Stat.
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for i := 0; i < 10; i++ {
		db.Audit("u", "a", "t")
	}

	report, err := db.DBUsage(dbPath)
	if err != nil {
		t.Fatalf("DBUsage: %v", err)
	}
	if report.FileBytes == 0 {
		t.Error("FileBytes = 0; expected a non-empty main database file")
	}
	names := map[string]bool{}
	descs := map[string]string{}
	for _, tb := range report.Tables {
		names[tb.Name] = true
		descs[tb.Name] = tb.Description
		if tb.Name == "audit_logs" && tb.Rows < 10 {
			t.Errorf("audit_logs rows = %d, want >= 10", tb.Rows)
		}
	}
	for _, want := range []string{"audit_logs", "users", "groups"} {
		if !names[want] {
			t.Errorf("DBUsage omitted table %q", want)
		}
	}
	// Every listed table should carry a purpose note (tableDescriptions is the
	// page's single source of truth, so a blank description means a table
	// slipped into knownTableSet without a doc entry).
	for name := range names {
		if descs[name] == "" {
			t.Errorf("table %q listed in DBUsage but has no description", name)
		}
	}
	// Spot-check: the direct lookup agrees with the report's field.
	if got := TableDescription("audit_logs"); got == "" {
		t.Error(`TableDescription("audit_logs") = "", want a non-empty note`)
	}
	if got := TableDescription("does_not_exist"); got != "" {
		t.Errorf(`TableDescription(unknown) = %q, want ""`, got)
	}
}
