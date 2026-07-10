package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetdiskCopyOne(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcDir, 0750); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0640); err != nil {
		t.Fatal(err)
	}

	dstFile := filepath.Join(dstDir, "a.txt")
	if err := netdiskCopyOne(srcFile, dstFile, false, "overwrite"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got, err := os.ReadFile(dstFile); err != nil || string(got) != "hello" {
		t.Fatalf("copy result = %q, %v", got, err)
	}

	dstFile2 := filepath.Join(dstDir, "renamed.txt")
	if err := netdiskCopyOne(srcFile, dstFile2, true, "rename"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(srcFile); err == nil {
		t.Error("source still exists after move")
	}
}

func TestNetdiskCopyOneIntoDirectory(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Copy a file into an existing directory.
	srcFile := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := netdiskCopyOne(srcFile, dstDir, false, "overwrite"); err != nil {
		t.Fatalf("copy into dir: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "a.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("copy into dir result = %q, %v", got, err)
	}

	// Move a file into an existing directory.
	srcFile2 := filepath.Join(srcDir, "b.txt")
	if err := os.WriteFile(srcFile2, []byte("world"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := netdiskCopyOne(srcFile2, dstDir, true, "rename"); err != nil {
		t.Fatalf("move into dir: %v", err)
	}
	if _, err := os.Stat(srcFile2); err == nil {
		t.Error("source still exists after move into dir")
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "b.txt")); err != nil || string(got) != "world" {
		t.Fatalf("move into dir result = %q, %v", got, err)
	}
}

func TestIdOwnedByRejectsEmpty(t *testing.T) {
	owned := map[string]bool{"abc123": true}
	if idOwnedBy(owned, "") {
		t.Error("idOwnedBy should reject empty id")
	}
	if !idOwnedBy(owned, "abc123") {
		t.Error("idOwnedBy should accept owned id")
	}
	if idOwnedBy(nil, "") {
		t.Error("idOwnedBy(nil, \"\") should reject empty id")
	}
}
