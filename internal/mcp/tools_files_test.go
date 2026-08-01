package mcp

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"path"
	"strings"
	"testing"
)

func TestSearchLines_SingleMatch(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	hits := searchLines("/f.txt", content, "beta", false)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Line != 2 || hits[0].Content != "beta" {
		t.Errorf("hit = %+v, want line 2 / content beta", hits[0])
	}
}

func TestSearchLines_MultipleMatches(t *testing.T) {
	content := "foo\nbar\nfoo\nbaz\nfoo\n"
	hits := searchLines("/f", content, "foo", false)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	wantLines := []int{1, 3, 5}
	for i, w := range wantLines {
		if hits[i].Line != w {
			t.Errorf("hit %d line = %d, want %d", i, hits[i].Line, w)
		}
	}
}

func TestSearchLines_CaseInsensitive(t *testing.T) {
	content := "Hello\nHELLO\nhello\n"
	// "hello" should match all three lines when case-insensitive.
	hits := searchLines("/f", content, "hello", true)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3 (case-insensitive)", len(hits))
	}
	// Case-sensitive should only match the lowercase one.
	hits = searchLines("/f", content, "hello", false)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (case-sensitive)", len(hits))
	}
}

func TestSearchLines_NoMatch(t *testing.T) {
	hits := searchLines("/f", "alpha\nbeta\n", "zzz", false)
	if len(hits) != 0 {
		t.Errorf("expected no hits, got %v", hits)
	}
}

func TestSearchLines_EmptyNeedle(t *testing.T) {
	if hits := searchLines("/f", "alpha\n", "", false); hits != nil {
		t.Errorf("empty needle should return nil, got %v", hits)
	}
}

func TestSearchLines_PreservesContent(t *testing.T) {
	// The returned content is the matched line verbatim (no lowercasing), even
	// when case-insensitive matching is on.
	hits := searchLines("/f", "Hello World\n", "hello", true)
	if len(hits) != 1 || hits[0].Content != "Hello World" {
		t.Errorf("content should be preserved verbatim, got %+v", hits)
	}
}

func TestSearchLines_LastLineNoTrailingNewline(t *testing.T) {
	content := "a\nb"
	hits := searchLines("/f", content, "b", false)
	if len(hits) != 1 || hits[0].Line != 2 {
		t.Errorf("expected line 2, got %+v", hits)
	}
}

// makeArchive builds a tar with the given entries (name, typeflag, body) for
// testing rewriteArchive.
func makeArchive(entries []struct {
	name     string
	typeflag byte
	body     string
}) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0644, Size: int64(len(e.body)), Typeflag: e.typeflag}
		if e.typeflag == tar.TypeDir {
			hdr.Size = 0
			hdr.Name = strings.TrimSuffix(e.name, "/") + "/"
		}
		_ = tw.WriteHeader(hdr)
		if e.typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestRewriteArchive_RenamesRoot(t *testing.T) {
	src := makeArchive([]struct {
		name     string
		typeflag byte
		body     string
	}{
		{"src", tar.TypeDir, ""},
		{"src/a.txt", tar.TypeReg, "hello"},
		{"src/sub", tar.TypeDir, ""},
		{"src/sub/b.txt", tar.TypeReg, "world"},
	})
	out, err := rewriteArchive(bytes.NewReader(src), "src", "dst")
	if err != nil {
		t.Fatalf("rewriteArchive: %v", err)
	}
	// Walk the result and collect names.
	tr := tar.NewReader(bytes.NewReader(out))
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
	}
	want := map[string]bool{"dst/": true, "dst/a.txt": true, "dst/sub/": true, "dst/sub/b.txt": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected entry %q", n)
		}
	}
	for n := range want {
		found := false
		for _, got := range names {
			if got == n {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected entry %q in %v", n, names)
		}
	}
}

func TestRewriteArchive_DropsSymlinks(t *testing.T) {
	src := makeArchive([]struct {
		name     string
		typeflag byte
		body     string
	}{
		{"f", tar.TypeReg, "data"},
		{"link", tar.TypeSymlink, ""},
	})
	out, err := rewriteArchive(bytes.NewReader(src), "f", "g")
	if err != nil {
		t.Fatalf("rewriteArchive: %v", err)
	}
	tr := tar.NewReader(bytes.NewReader(out))
	count := 0
	for {
		_, err := tr.Next()
		if err != nil {
			break
		}
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 entry (symlink dropped), got %d", count)
	}
}

func TestRewriteArchive_StampedServiceOwnership(t *testing.T) {
	// copy_file re-uploads headers copied from the container, which are
	// typically root-owned (uid/gid 0). When the destination is under the
	// bind-mounted /workspace that would leave root-owned files on the host,
	// so rewriteArchive must overwrite ownership with the service uid/gid.
	const rootUid, rootGid = 0, 0
	src := bytes.NewBuffer(nil)
	tw := tar.NewWriter(src)
	entries := []struct {
		name     string
		typeflag byte
	}{
		{"src", tar.TypeDir},
		{"src/a.txt", tar.TypeReg},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0644, Typeflag: e.typeflag, Uid: rootUid, Gid: rootGid}
		if e.typeflag == tar.TypeDir {
			hdr.Name = strings.TrimSuffix(e.name, "/") + "/"
		} else {
			hdr.Size = int64(len("hello"))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte("hello"))
		}
	}
	_ = tw.Close()

	out, err := rewriteArchive(bytes.NewReader(src.Bytes()), "src", "dst")
	if err != nil {
		t.Fatalf("rewriteArchive: %v", err)
	}
	uid, gid := serviceUid, serviceGid
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Uid != uid || hdr.Gid != gid {
			t.Errorf("entry %q ownership = uid:%d gid:%d (source was root-owned), want uid:%d gid:%d",
				hdr.Name, hdr.Uid, hdr.Gid, uid, gid)
		}
	}
}

func TestBase64RoundTrip(t *testing.T) {
	payloads := [][]byte{
		[]byte("plain text"),
		{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, // PNG header bytes
		{0x00, 0x01, 0x02, 0xFE, 0xFF},
		make([]byte, 4096),
	}
	for i, raw := range payloads {
		enc := base64.StdEncoding.EncodeToString(raw)
		dec, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("payload %d: decode error: %v", i, err)
		}
		if !bytes.Equal(dec, raw) {
			t.Errorf("payload %d: round-trip mismatch", i)
		}
	}
}

func TestBuildPathTar_CreatesDirEntries(t *testing.T) {
	out := buildPathTar("/a/b/c.txt", []byte("data"))
	tr := tar.NewReader(bytes.NewReader(out))
	var dirs []string
	var fileName string
	var fileBody string
	uid, gid := serviceUid, serviceGid
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		// Every entry must carry the service uid/gid, not the 0 default, so
		// files written into the bind-mounted /workspace land owned by the
		// mudp user on the host instead of root.
		if hdr.Uid != uid || hdr.Gid != gid {
			t.Errorf("entry %q ownership = uid:%d gid:%d, want uid:%d gid:%d", hdr.Name, hdr.Uid, hdr.Gid, uid, gid)
		}
		if hdr.Typeflag == tar.TypeDir {
			dirs = append(dirs, hdr.Name)
		} else if hdr.Typeflag == tar.TypeReg {
			fileName = hdr.Name
			buf := make([]byte, hdr.Size)
			_, _ = tr.Read(buf)
			fileBody = string(buf)
		}
	}
	// /a/b/c.txt -> ancestor dirs are "a" and "a/b"; both must be present so
	// CopyToContainer creates them without mkdir.
	if len(dirs) != 2 {
		t.Fatalf("expected 2 ancestor dirs, got %d: %v", len(dirs), dirs)
	}
	wantDirs := map[string]bool{"a/": true, "a/b/": true}
	for _, d := range dirs {
		if !wantDirs[d] {
			t.Errorf("unexpected dir entry %q", d)
		}
	}
	// The file entry must carry its full path relative to the upload root; a bare
	// basename would land at "/" and drop the parent directories.
	if fileName != "a/b/c.txt" {
		t.Errorf("file name = %q, want a/b/c.txt", fileName)
	}
	if fileBody != "data" {
		t.Errorf("file body = %q, want data", fileBody)
	}
}

func TestBuildPathTar_TopLevelFile(t *testing.T) {
	// /app.py has no parent dir segments, so no dir entries should be emitted.
	out := buildPathTar("/app.py", []byte("print(1)"))
	tr := tar.NewReader(bytes.NewReader(out))
	dirCount := 0
	fileCount := 0
	uid, gid := serviceUid, serviceGid
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		// Even a top-level file must be stamped with the service ownership.
		if hdr.Uid != uid || hdr.Gid != gid {
			t.Errorf("entry %q ownership = uid:%d gid:%d, want uid:%d gid:%d", hdr.Name, hdr.Uid, hdr.Gid, uid, gid)
		}
		if hdr.Typeflag == tar.TypeDir {
			dirCount++
		} else {
			fileCount++
		}
	}
	if dirCount != 0 {
		t.Errorf("expected 0 dirs for top-level file, got %d", dirCount)
	}
	if fileCount != 1 {
		t.Errorf("expected 1 file entry, got %d", fileCount)
	}
}

func TestBuildPathTar_EntryNamesRelativeToRoot(t *testing.T) {
	// Entries must be relative (no leading slash) so CopyToContainer at "/"
	// places them correctly.
	out := buildPathTar("/x/y/z", []byte("k"))
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasPrefix(hdr.Name, "/") {
			t.Errorf("entry name %q has leading slash — must be relative", hdr.Name)
		}
		if strings.Contains(hdr.Name, path.Clean("/x/y/z")+"/") {
			// fine, this is an ancestor dir
		}
	}
}
