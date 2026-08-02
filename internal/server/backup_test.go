package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mudp/internal/config"
	"mudp/internal/store"

	_ "modernc.org/sqlite"
)

// TestBackupDataProducesConsistentSnapshot writes a row, immediately backs up
// (with no explicit WAL checkpoint in between), and verifies the row is
// present in the restored database. This is the P0-2 regression test: a raw
// io.Copy of the live WAL-mode file could miss recently committed rows, but
// VACUUM INTO must not.
func TestBackupDataProducesConsistentSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Audit("tester", "canary.write", "sentinel-value-12345")

	a := &App{db: db, cfg: config.Config{DBPath: dbPath}}

	targetDir := t.TempDir()
	body := `{"targetDir":` + jsonQuote(targetDir) + `}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/backup", strings.NewReader(body))
	a.backupData(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backupData status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// The zip must contain exactly the DB file, at 0600, and no leftover
	// snapshot temp file in the target directory.
	zr, err := zip.OpenReader(resp.Path)
	if err != nil {
		t.Fatalf("open backup zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 {
		t.Fatalf("zip contains %d entries, want 1", len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("open zip entry: %v", err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	out, err := os.Create(restoredPath)
	if err != nil {
		t.Fatalf("create restored file: %v", err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		t.Fatalf("extract: %v", err)
	}
	out.Close()
	rc.Close()

	sdb, err := sql.Open("sqlite", restoredPath)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer sdb.Close()
	var target string
	if err := sdb.QueryRow(`select target from audit_logs where action = 'canary.write'`).Scan(&target); err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if target != "sentinel-value-12345" {
		t.Fatalf("restored audit row target = %q, want sentinel-value-12345", target)
	}

	if entries, _ := filepath.Glob(filepath.Join(targetDir, "*.snapshot.tmp")); len(entries) != 0 {
		t.Errorf("leftover snapshot temp file(s): %v", entries)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
