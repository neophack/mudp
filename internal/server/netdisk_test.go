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

// TestShareVisibleAncestor covers the "shared multiple files inside one folder"
// regression: the folder itself must appear in the listing so the visitor can
// navigate to the files, while unshared siblings must stay hidden.
func TestShareVisibleAncestor(t *testing.T) {
	paths := []string{"sub/a.txt", "sub/b.txt"}

	cases := []struct {
		req string
		// wantVisible is the expected shareVisible result (listing).
		wantVisible bool
		// wantContains is the expected shareContains result (download/save).
		wantContains bool
	}{
		{"sub", true, false},        // parent folder: visible, not directly downloadable
		{"sub/a.txt", true, true},   // shared file: visible and downloadable
		{"sub/b.txt", true, true},   // shared file: visible and downloadable
		{"sub/c.txt", false, false}, // unshared sibling: hidden
		{"other", false, false},     // unrelated folder: hidden
		{"a.txt", false, false},     // root file with same name: hidden
		{"", false, false},          // empty request: never visible
	}
	for _, c := range cases {
		if got := shareVisible(paths, c.req); got != c.wantVisible {
			t.Errorf("shareVisible(%q) = %v, want %v", c.req, got, c.wantVisible)
		}
		if got := shareContains(paths, c.req); got != c.wantContains {
			t.Errorf("shareContains(%q) = %v, want %v", c.req, got, c.wantContains)
		}
	}
}

// TestShareVisibleMixedRootAndNested ensures a folder shared alongside files in
// another folder lists all of its descendants without leaking across folders.
// TestFlatShareRootItems verifies the share root listing: a file shared from
// inside a folder is shown flat (by its base name, no parent folder), while a
// shared folder appears as a navigable folder row. Unshared siblings never
// surface because the root no longer lists a real directory.
func TestFlatShareRootItems(t *testing.T) {
	tmp := t.TempDir()
	// Layout:
	//   docs/a.txt   (shared)
	//   docs/b.txt   (shared)
	//   docs/c.txt   (NOT shared — must never appear)
	//   photos/      (shared folder)
	//   photos/x.jpg
	//   other.txt    (NOT shared)
	if err := os.MkdirAll(filepath.Join(tmp, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "photos"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "docs", "a.txt"), []byte("a"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "docs", "b.txt"), []byte("bb"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "docs", "c.txt"), []byte("ccc"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "photos", "x.jpg"), []byte("jpg"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "other.txt"), []byte("o"), 0640); err != nil {
		t.Fatal(err)
	}

	paths := []string{"docs/a.txt", "docs/b.txt", "photos"}
	items := flatShareRootItems(tmp, paths)

	// Expect exactly: a.txt, b.txt, photos — no docs folder, no c.txt, no other.txt.
	want := map[string]struct{ dir bool }{
		"a.txt":  {false},
		"b.txt":  {false},
		"photos": {true},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for _, it := range items {
		exp, ok := want[it.Name]
		if !ok {
			t.Errorf("unexpected root item %q (path=%q)", it.Name, it.Path)
			continue
		}
		if it.Dir != exp.dir {
			t.Errorf("item %q dir=%v, want %v", it.Name, it.Dir, exp.dir)
		}
	}
	// Shared file rows must keep their full shared path so download/save work.
	aRow := findItem(items, "a.txt")
	if aRow == nil || aRow.Path != "docs/a.txt" {
		t.Errorf("a.txt path = %q, want \"docs/a.txt\"", itemPath(items, "a.txt"))
	}
	// The shared folder must be navigable.
	pRow := findItem(items, "photos")
	if pRow == nil || !pRow.Dir || pRow.Path != "photos" {
		t.Errorf("photos row = %+v, want navigable folder with path \"photos\"", pRow)
	}
}

func findItem(items []fileItem, name string) *fileItem {
	for i := range items {
		if items[i].Name == name {
			return &items[i]
		}
	}
	return nil
}

func itemPath(items []fileItem, name string) string {
	if it := findItem(items, name); it != nil {
		return it.Path
	}
	return ""
}
