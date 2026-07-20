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

	wantDir := filepath.Join(root, openID+"-"+userID)
	if st, err := os.Stat(wantDir); err != nil || !st.IsDir() {
		t.Fatalf("expected user dir %q to exist and be a directory, err=%v", wantDir, err)
	}

	badDir := filepath.Join(root, openID+"-%!d(string=2)")
	if _, err := os.Stat(badDir); err == nil {
		t.Fatalf("unexpected malformed dir created: %q", badDir)
	}
}
