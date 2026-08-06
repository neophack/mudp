package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureUserNetdiskDirUsesStringUserIDInFolderName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on Windows to avoid COM shortcut dependency in test")
	}
	root := t.TempDir()
	openID := "ou_434e4f4cfc471a58d6909409861e2ad8"
	userID := "2"
	displayName := "test-user"

	if err := EnsureUserNetdiskDir(root, openID, userID, displayName); err != nil {
		t.Fatalf("EnsureUserNetdiskDir: %v", err)
	}

	// The real directory is named from the (pinyin-slugified) display name,
	// not the raw account identifier, so it stays human-readable.
	wantDir := filepath.Join(root, displayName+"-"+userID)
	if st, err := os.Stat(wantDir); err != nil || !st.IsDir() {
		t.Fatalf("expected user dir %q to exist and be a directory, err=%v", wantDir, err)
	}

	badDir := filepath.Join(root, displayName+"-%!d(string=2)")
	if _, err := os.Stat(badDir); err == nil {
		t.Fatalf("unexpected malformed dir created: %q", badDir)
	}
}

func TestEnsureUserNetdiskDirMigratesLegacyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on Windows to avoid COM shortcut dependency in test")
	}
	root := t.TempDir()
	openID := "ouad8aecd70597ed840a1202ec33a691d8"
	userID := "7"
	displayName := "陈怡欣"

	legacyDir := filepath.Join(root, openID+"-"+userID)
	if err := os.MkdirAll(legacyDir, 0750); err != nil {
		t.Fatalf("seed legacy dir: %v", err)
	}
	marker := filepath.Join(legacyDir, "existing-file.txt")
	if err := os.WriteFile(marker, []byte("data"), 0644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	if err := EnsureUserNetdiskDir(root, openID, userID, displayName); err != nil {
		t.Fatalf("EnsureUserNetdiskDir: %v", err)
	}

	newDir := filepath.Join(root, "chenyixin-"+userID)
	if st, err := os.Stat(newDir); err != nil || !st.IsDir() {
		t.Fatalf("expected migrated user dir %q to exist and be a directory, err=%v", newDir, err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "existing-file.txt")); err != nil {
		t.Fatalf("expected pre-existing file to have moved with the directory: %v", err)
	}

	li, err := os.Lstat(legacyDir)
	if err != nil {
		t.Fatalf("expected legacy path to remain as a symlink: %v", err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected legacy path %q to become a symlink, got mode %v", legacyDir, li.Mode())
	}
	target, err := os.Readlink(legacyDir)
	if err != nil {
		t.Fatalf("readlink legacy path: %v", err)
	}
	if target != "chenyixin-"+userID {
		t.Fatalf("legacy symlink target = %q, want %q", target, "chenyixin-"+userID)
	}
}
