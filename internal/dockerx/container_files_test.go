package dockerx

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

// entriesToTar builds a tar archive from a list of (name, typeflag) pairs.
func entriesToTar(t *testing.T, entries []struct {
	name string
	flag byte
}) *tar.Reader {
	t.Helper()
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0755, Size: 0, Typeflag: e.flag}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return tar.NewReader(buf)
}

func TestParseContainerFileListDirectChildrenOnly(t *testing.T) {
	entries := []struct {
		name string
		flag byte
	}{
		{"root/", tar.TypeDir},
		{"root/a.txt", tar.TypeReg},
		{"root/sub/", tar.TypeDir},
		{"root/sub/b.txt", tar.TypeReg},
		{"root/sub/deep/", tar.TypeDir},
		{"root/sub/deep/c.txt", tar.TypeReg},
	}
	tr := entriesToTar(t, entries)
	got, err := parseContainerFileList("/root", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 direct children, got %d: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
		if e.Path != "/root/"+e.Name {
			t.Errorf("unexpected path %q for name %q", e.Path, e.Name)
		}
	}
	if !names["a.txt"] || !names["sub"] {
		t.Errorf("expected a.txt and sub, got %+v", names)
	}
}

func TestParseContainerFileListRoot(t *testing.T) {
	entries := []struct {
		name string
		flag byte
	}{
		{"./", tar.TypeDir},
		{"a.txt", tar.TypeReg},
		{"sub/", tar.TypeDir},
		{"sub/b.txt", tar.TypeReg},
	}
	tr := entriesToTar(t, entries)
	got, err := parseContainerFileList("/", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 direct children, got %d: %+v", len(got), got)
	}
}

func TestParseContainerFileListRootWithLeadingSlash(t *testing.T) {
	entries := []struct {
		name string
		flag byte
	}{
		{"/", tar.TypeDir},
		{"/a.txt", tar.TypeReg},
		{"/sub/", tar.TypeDir},
		{"/sub/b.txt", tar.TypeReg},
	}
	tr := entriesToTar(t, entries)
	got, err := parseContainerFileList("/", tr)
	if err != nil {
		t.Fatalf("parseContainerFileList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 direct children, got %d: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
		if e.Path != "/"+e.Name {
			t.Errorf("unexpected path %q for name %q", e.Path, e.Name)
		}
	}
	if !names["a.txt"] || !names["sub"] {
		t.Errorf("expected a.txt and sub, got %+v", names)
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
