package upgrader

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := map[[2]string]string{
		{"windows", "amd64"}: "mudp-windows-amd64.exe",
		{"linux", "amd64"}:   "mudp-linux-amd64",
		{"windows", "arm64"}: "mudp-windows-arm64.exe",
		{"linux", "arm64"}:   "mudp-linux-arm64",
		{"darwin", "amd64"}:  "",
		{"linux", "386"}:     "",
	}
	for platform, want := range cases {
		if got := AssetName(platform[0], platform[1]); got != want {
			t.Errorf("AssetName(%s) = %q, want %q", platform, got, want)
		}
	}
	if !Supported() && runtime.GOOS != "darwin" {
		// Every platform mudp documents as releasable must be supported.
		t.Errorf("expected %s/%s to be supported", runtime.GOOS, runtime.GOARCH)
	}
}

func TestArchiveName(t *testing.T) {
	cases := map[[2]string]string{
		{"windows", "amd64"}: "mudp-windows-amd64-v1.2.0.zip",
		{"linux", "amd64"}:   "mudp-linux-amd64-v1.2.0.tar.gz",
		{"windows", "arm64"}: "mudp-windows-arm64-v1.2.0.zip",
		{"linux", "arm64"}:   "mudp-linux-arm64-v1.2.0.tar.gz",
	}
	for platform, want := range cases {
		if got := ArchiveName("v1.2.0", platform[0], platform[1]); got != want {
			t.Errorf("ArchiveName(%s) = %q, want %q", platform, got, want)
		}
	}
}

func TestAssetURL(t *testing.T) {
	url, err := AssetURL("v1.2.0", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/neophack/mudp/releases/download/v1.2.0/mudp-linux-amd64-v1.2.0.tar.gz"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
	if _, err := AssetURL("v1.2.0", "darwin", "arm64"); err == nil {
		t.Fatal("darwin should be unsupported")
	}
}

// requireExecBit asserts ExtractBinary's final os.Chmod(dest, 0o755) took
// effect: on Linux an upgrade swapped in without the exec bit is
// permission-denied at exec time and rolls back forever. Skipped on Windows:
// Go's Windows Chmod only toggles the read-only attribute and Stat().Mode()
// stays 0666 (verified on go1.25.5/windows-amd64), so the exec-bit check
// cannot work there.
func requireExecBit(t *testing.T, dest string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("extracted binary %s must be executable, got mode %v", dest, fi.Mode())
	}
}

func TestExtractBinary(t *testing.T) {
	payload := []byte("fake mudp binary")
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.new")

	// zip (windows-style release archive)
	zipPath := filepath.Join(dir, "mudp-windows-amd64-v1.2.0.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("mudp-windows-amd64.exe")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(payload)
	zw.Close()
	zf.Close()
	if err := ExtractBinary(zipPath, dest, "windows", "amd64"); err != nil {
		t.Fatalf("extract zip: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("zip extract got %q, %v", got, err)
	}
	requireExecBit(t, dest)

	// Regression: runUpgrade stores the download at SidecarPath(exe,
	// ".download"), which keeps the .exe extension on Windows. The extractor
	// must still treat it as a zip (format follows the platform's asset, not
	// the temp file name) instead of failing with "gzip: invalid header".
	sidecar := SidecarPath(filepath.Join(dir, "mudp.exe"), ".download")
	if filepath.Ext(sidecar) != ".exe" {
		t.Fatalf("test setup: sidecar %q should end in .exe", sidecar)
	}
	if err := os.Rename(zipPath, sidecar); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(dir, "out-sidecar.new")
	if err := ExtractBinary(sidecar, dest, "windows", "amd64"); err != nil {
		t.Fatalf("extract zip from .exe-named sidecar: %v", err)
	}
	got, err = os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("sidecar zip extract got %q, %v", got, err)
	}
	requireExecBit(t, dest)

	// tar.gz (linux-style release archive)
	dest = filepath.Join(dir, "out2.new")
	tgzPath := filepath.Join(dir, "mudp-linux-amd64-v1.2.0.tar.gz")
	tf, err := os.Create(tgzPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(tf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: "mudp-linux-amd64", Mode: 0o755, Size: int64(len(payload))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Write(payload)
	tw.Close()
	gw.Close()
	tf.Close()
	if err := ExtractBinary(tgzPath, dest, "linux", "amd64"); err != nil {
		t.Fatalf("extract tar.gz: %v", err)
	}
	got, err = os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("tar.gz extract got %q, %v", got, err)
	}
	requireExecBit(t, dest)

	// An archive with no usable member must be rejected, not silently
	// swapped in.
	emptyZip := filepath.Join(dir, "empty.zip")
	ef, err := os.Create(emptyZip)
	if err != nil {
		t.Fatal(err)
	}
	zip.NewWriter(ef).Close()
	ef.Close()
	if err := ExtractBinary(emptyZip, filepath.Join(dir, "out3.new"), "windows", "amd64"); err == nil {
		t.Fatal("empty archive should fail")
	}
}

// TestExtractBinaryPrefersNamedMember: a release archive may carry extras
// (README.md, licenses) next to the binary. The member whose base name matches
// AssetName must win over the "first regular member" fallback — extracting the
// README instead would swap in a non-executable payload and fail forever. The
// binary also rides in a subdirectory to pin the filepath.Base name comparison.
func TestExtractBinaryPrefersNamedMember(t *testing.T) {
	payload := []byte("real mudp binary payload")
	readme := []byte("# mudp release notes\nnot a binary\n")
	dir := t.TempDir()

	// zip (windows-style): README.md first, named binary second.
	zipPath := filepath.Join(dir, "mudp-windows-amd64-v1.2.0.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("README.md")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(readme)
	w, err = zw.Create("bin/mudp-windows-amd64.exe")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(payload)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "zip-named.new")
	if err := ExtractBinary(zipPath, dest, "windows", "amd64"); err != nil {
		t.Fatalf("extract zip: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("zip picked the wrong member: got %q, %v", got, err)
	}
	requireExecBit(t, dest)

	// tar.gz (linux-style): same shape, README.md first.
	tgzPath := filepath.Join(dir, "mudp-linux-amd64-v1.2.0.tar.gz")
	tf, err := os.Create(tgzPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(tf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: "README.md", Typeflag: tar.TypeReg, Size: int64(len(readme))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Write(readme)
	hdr = &tar.Header{Name: "mudp-linux-amd64", Mode: 0o755, Size: int64(len(payload))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Write(payload)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tf.Close(); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(dir, "tar-named.new")
	if err := ExtractBinary(tgzPath, dest, "linux", "amd64"); err != nil {
		t.Fatalf("extract tar.gz: %v", err)
	}
	got, err = os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("tar.gz picked the wrong member: got %q, %v", got, err)
	}
	requireExecBit(t, dest)
}

func TestDownloadWritesFileAndRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("binary-bytes"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "mudp.new")
	var lastRead, lastTotal int64
	onProgress := func(read, total int64) { lastRead, lastTotal = read, total }
	if err := Download(context.Background(), srv.URL+"/good", dest, onProgress); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "binary-bytes" {
		t.Fatalf("downloaded %q", data)
	}
	if lastRead != int64(len("binary-bytes")) || lastTotal != int64(len("binary-bytes")) {
		t.Fatalf("progress = %d/%d, want %d/%d", lastRead, lastTotal, len("binary-bytes"), len("binary-bytes"))
	}
	if err := Download(context.Background(), srv.URL+"/bad", dest, nil); err == nil {
		t.Fatal("404 should error")
	}
}

func TestSidecarPath(t *testing.T) {
	if got := SidecarPath("C:\\mudp\\mudp.exe", ".bak"); got != "C:\\mudp\\mudp.bak.exe" {
		t.Errorf("windows sidecar = %q", got)
	}
	if got := SidecarPath("/opt/mudp/mudp", ".bak"); got != "/opt/mudp/mudp.bak" {
		t.Errorf("linux sidecar = %q", got)
	}
}

func TestSwapRollbackCommit(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mudp")
	if runtime.GOOS == "windows" {
		exe = filepath.Join(dir, "mudp.exe")
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(exe, "old")
	newPath := SidecarPath(exe, ".new")
	write(newPath, "new")

	if err := WriteMarker(exe, Marker{From: "v1", To: "v2"}); err != nil {
		t.Fatal(err)
	}
	if m := ReadMarker(exe); m == nil || m.To != "v2" {
		t.Fatalf("marker roundtrip failed: %+v", m)
	}

	if err := Swap(exe, newPath); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new" {
		t.Fatalf("after swap exe = %q, want new", got)
	}
	bak, _ := os.ReadFile(SidecarPath(exe, ".bak"))
	if string(bak) != "old" {
		t.Fatalf("after swap bak = %q, want old", bak)
	}

	// Failed upgrade: rollback restores the old binary and clears the marker.
	if err := Rollback(exe); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(exe)
	if string(got) != "old" {
		t.Fatalf("after rollback exe = %q, want old", got)
	}
	if m := ReadMarker(exe); m != nil {
		t.Fatalf("rollback should clear the marker, got %+v", m)
	}
	if _, err := os.Stat(SidecarPath(exe, ".failed")); err != nil {
		t.Fatalf("failed binary should be parked: %v", err)
	}

	// Successful upgrade: swap again, then commit drops backup and marker.
	write(newPath, "new2")
	write(exe, "old")
	if err := WriteMarker(exe, Marker{From: "v1", To: "v3"}); err != nil {
		t.Fatal(err)
	}
	if err := Swap(exe, newPath); err != nil {
		t.Fatal(err)
	}
	if err := Commit(exe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SidecarPath(exe, ".bak")); !os.IsNotExist(err) {
		t.Fatalf("commit should drop the backup")
	}
	if m := ReadMarker(exe); m != nil {
		t.Fatalf("commit should drop the marker")
	}
}
