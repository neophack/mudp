package dockerx

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

// tarEntry is one tar header written by entriesToTar. Non-zero sizes get
// zero-filled bodies so the archive stays readable.
type tarEntry struct {
	name string
	flag byte
	mode int64
	size int64
}

// entriesToTar builds a tar archive from the given entries.
func entriesToTar(t *testing.T, entries []tarEntry) *tar.Reader {
	t.Helper()
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: e.size, Typeflag: e.flag}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if e.size > 0 {
			if _, err := tw.Write(make([]byte, e.size)); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return tar.NewReader(buf)
}

func TestParseContainerFileListDirectChildrenOnly(t *testing.T) {
	tr := entriesToTar(t, []tarEntry{
		{"root/", tar.TypeDir, 0755, 0},
		{"root/a.txt", tar.TypeReg, 0644, 12},
		{"root/sub/", tar.TypeDir, 0750, 0},
		{"root/sub/b.txt", tar.TypeReg, 0600, 3},
		{"root/sub/deep/", tar.TypeDir, 0755, 0},
		{"root/sub/deep/c.txt", tar.TypeReg, 0644, 0},
	})
	got, err := parseContainerFileList("/root", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	want := map[string]struct {
		dir  bool
		size int64
		mode string
	}{
		"a.txt": {false, 12, "-rw-r--r--"},
		"sub":   {true, 0, "drwxr-x---"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d direct children, got %d: %+v", len(want), len(got), got)
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok {
			t.Errorf("unexpected entry %q", e.Name)
			continue
		}
		if e.Path != "/root/"+e.Name {
			t.Errorf("unexpected path %q for name %q", e.Path, e.Name)
		}
		if e.Dir != w.dir {
			t.Errorf("%s: dir = %v, want %v", e.Name, e.Dir, w.dir)
		}
		if e.Size != w.size {
			t.Errorf("%s: size = %d, want %d", e.Name, e.Size, w.size)
		}
		if e.Mode != w.mode {
			t.Errorf("%s: mode = %q, want %q", e.Name, e.Mode, w.mode)
		}
	}
}

func TestParseContainerFileListRoot(t *testing.T) {
	tr := entriesToTar(t, []tarEntry{
		{"./", tar.TypeDir, 0755, 0},
		{"a.txt", tar.TypeReg, 0644, 7},
		{"sub/", tar.TypeDir, 0700, 0},
		{"sub/b.txt", tar.TypeReg, 0644, 0},
	})
	got, err := parseContainerFileList("/", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	want := map[string]struct {
		dir  bool
		size int64
		mode string
	}{
		"a.txt": {false, 7, "-rw-r--r--"},
		"sub":   {true, 0, "drwx------"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d direct children, got %d: %+v", len(want), len(got), got)
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok {
			t.Errorf("unexpected entry %q", e.Name)
			continue
		}
		if e.Path != "/"+e.Name {
			t.Errorf("unexpected path %q for name %q", e.Path, e.Name)
		}
		if e.Dir != w.dir {
			t.Errorf("%s: dir = %v, want %v", e.Name, e.Dir, w.dir)
		}
		if e.Size != w.size {
			t.Errorf("%s: size = %d, want %d", e.Name, e.Size, w.size)
		}
		if e.Mode != w.mode {
			t.Errorf("%s: mode = %q, want %q", e.Name, e.Mode, w.mode)
		}
	}
}

func TestParseContainerFileListRootWithLeadingSlash(t *testing.T) {
	tr := entriesToTar(t, []tarEntry{
		{"/", tar.TypeDir, 0755, 0},
		{"/a.txt", tar.TypeReg, 0644, 7},
		{"/sub/", tar.TypeDir, 0700, 0},
		{"/sub/b.txt", tar.TypeReg, 0644, 0},
	})
	got, err := parseContainerFileList("/", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	want := map[string]struct {
		dir  bool
		size int64
		mode string
	}{
		"a.txt": {false, 7, "-rw-r--r--"},
		"sub":   {true, 0, "drwx------"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d direct children, got %d: %+v", len(want), len(got), got)
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok {
			t.Errorf("unexpected entry %q", e.Name)
			continue
		}
		if e.Path != "/"+e.Name {
			t.Errorf("unexpected path %q for name %q", e.Path, e.Name)
		}
		if e.Dir != w.dir {
			t.Errorf("%s: dir = %v, want %v", e.Name, e.Dir, w.dir)
		}
		if e.Size != w.size {
			t.Errorf("%s: size = %d, want %d", e.Name, e.Size, w.size)
		}
		if e.Mode != w.mode {
			t.Errorf("%s: mode = %q, want %q", e.Name, e.Mode, w.mode)
		}
	}
}

// formatFileMode renders the 9 permission bits plus a type character; setuid,
// setgid and sticky bits are not part of the rendering.
func TestFormatFileMode(t *testing.T) {
	cases := []struct {
		name     string
		typeflag byte
		mode     int64
		want     string
	}{
		{"directory", tar.TypeDir, 0755, "drwxr-xr-x"},
		{"regular file", tar.TypeReg, 0644, "-rw-r--r--"},
		{"owner-only file", tar.TypeReg, 0600, "-rw-------"},
		{"all perms", tar.TypeReg, 0777, "-rwxrwxrwx"},
		{"no perms", tar.TypeReg, 0000, "----------"},
		{"symlink", tar.TypeSymlink, 0777, "lrwxrwxrwx"},
		{"char device", tar.TypeChar, 0600, "crw-------"},
		{"block device", tar.TypeBlock, 0660, "brw-rw----"},
		{"fifo", tar.TypeFifo, 0644, "prw-r--r--"},
		{"unknown type falls back to file", tar.TypeXGlobalHeader, 0644, "-rw-r--r--"},
	}
	for _, c := range cases {
		if got := formatFileMode(c.typeflag, c.mode); got != c.want {
			t.Errorf("%s: formatFileMode(%#o, %d) = %q, want %q", c.name, c.mode, c.typeflag, got, c.want)
		}
	}
}

func TestParseExecFileList(t *testing.T) {
	output := "drwxr-xr-x|4096|1699123456|/bin\n" +
		"drwxr-xr-x|4096|1699123456|/etc\n" +
		"lrwxrwxrwx|0|1699123456|/lib\n" +
		"\n" +
		"invalid-line\n"
	got := parseExecFileList("/", output)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(got), got)
	}
	want := []struct {
		name string
		path string
		dir  bool
		size int64
		mode string
	}{
		{"bin", "/bin", true, 4096, "drwxr-xr-x"},
		{"etc", "/etc", true, 4096, "drwxr-xr-x"},
		{"lib", "/lib", false, 0, "lrwxrwxrwx"},
	}
	for i, e := range got {
		if e.Name != want[i].name || e.Path != want[i].path || e.Dir != want[i].dir || e.Size != want[i].size || e.Mode != want[i].mode {
			t.Errorf("entry %d: got %+v, want %+v", i, e, want[i])
		}
	}
}

func TestParseExecFileListSubdir(t *testing.T) {
	output := "drwxr-xr-x|4096|1699123456|/etc/cron.d\n" +
		"-rw-r--r--|123|1699123456|/etc/hosts\n"
	got := parseExecFileList("/etc", output)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].Name != "cron.d" || got[0].Path != "/etc/cron.d" {
		t.Errorf("unexpected first entry: %+v", got[0])
	}
	if got[1].Name != "hosts" || got[1].Path != "/etc/hosts" {
		t.Errorf("unexpected second entry: %+v", got[1])
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/path", "'/path'"},
		{"/path with spaces", "'/path with spaces'"},
		{"/path'quote", "'/path'\\''quote'"},
	}
	for _, c := range cases {
		got := shellQuote(c.in)
		if got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRootListCandidatesIncludesWorkingDirAndMounts(t *testing.T) {
	got := rootListCandidates("/custom/work", []string{"/mnt/data", "/workspace/project", "/custom/cache"})
	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
		if strings.Contains(name, "/") {
			t.Fatalf("candidate %q should be a root child only", name)
		}
	}
	for _, want := range []string{"bin", "custom", "mnt", "workspace"} {
		if !seen[want] {
			t.Errorf("missing root candidate %q in %+v", want, got)
		}
	}
}
