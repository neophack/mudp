package mcp

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"

	"mudp/internal/dockerx"
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
	// Walk the result and collect names plus file bodies: the rename must move
	// the content along with the entries, not just relabel empty headers.
	tr := tar.NewReader(bytes.NewReader(out))
	bodies := map[string]string{}
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
		if hdr.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read body of %q: %v", hdr.Name, err)
			}
			bodies[hdr.Name] = string(body)
		}
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
	if got := bodies["dst/a.txt"]; got != "hello" {
		t.Errorf("dst/a.txt body = %q, want hello", got)
	}
	if got := bodies["dst/sub/b.txt"]; got != "world" {
		t.Errorf("dst/sub/b.txt body = %q, want world", got)
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
	// Parse the rewritten archive instead of counting entries: an inverted
	// filter (links kept, files dropped) would also produce exactly one entry.
	tr := tar.NewReader(bytes.NewReader(out))
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			t.Errorf("entry %q is a link; links must be dropped", hdr.Name)
		}
		names = append(names, hdr.Name)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(names), names)
	}
	if names[0] != "g" {
		t.Errorf("surviving entry = %q, want g (the regular file under its new name)", names[0])
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

// fakeDockerArchive is a minimal stand-in for the Docker archive API that
// handleUploadFile/handleDownloadFile are built on, so the full transfer path
// can be exercised without a daemon: PUT extracts the posted tar into an
// in-memory path→content map, GET serves one file back as a single-entry tar,
// HEAD answers the stat probe the SDK issues alongside both calls.
type fakeDockerArchive struct {
	files map[string][]byte
}

func (f *fakeDockerArchive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/_ping") {
		w.Header().Set("Api-Version", "1.44")
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/archive") {
		http.NotFound(w, r)
		return
	}
	p := r.URL.Query().Get("path")
	body, ok := f.files[p]
	stat, _ := json.Marshal(types.ContainerPathStat{Name: path.Base(p), Mode: 0o644, Size: int64(len(body))})
	switch r.Method {
	case http.MethodHead:
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: path.Base(p), Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = tw.Write(body)
		_ = tw.Close()
		_, _ = w.Write(buf.Bytes())
	case http.MethodPut:
		tr := tar.NewReader(r.Body)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			if hdr.Typeflag != tar.TypeReg {
				continue
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.files[path.Join("/", p, strings.TrimPrefix(hdr.Name, "./"))] = data
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// TestUploadDownloadFileRoundTrip drives upload_file then download_file through
// a fake container: the payload must land byte-identical at the exact path and
// come back unchanged, and a payload over the size cap must be refused before
// anything is written.
func TestUploadDownloadFileRoundTrip(t *testing.T) {
	fake := &fakeDockerArchive{files: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dc, err := dockerx.NewWithHost("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	ctx := context.Background()

	payload := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF}
	args, _ := json.Marshal(uploadArgs{
		Path:          "/tmp/shot.png",
		ContentBase64: base64.StdEncoding.EncodeToString(payload),
	})
	res, err := handleUploadFile(ctx, dc, "c1", args)
	if err != nil || res.IsError {
		t.Fatalf("upload: err=%v isError=%v", err, res.IsError)
	}
	if got := fake.files["/tmp/shot.png"]; !bytes.Equal(got, payload) {
		t.Fatalf("container content = %v, want the original %d bytes", got, len(payload))
	}

	args, _ = json.Marshal(downloadArgs{Path: "/tmp/shot.png"})
	res, err = handleDownloadFile(ctx, dc, "c1", args)
	if err != nil || res.IsError {
		t.Fatalf("download: err=%v isError=%v", err, res.IsError)
	}
	var reply struct {
		Path   string `json:"path"`
		Size   int    `json:"size"`
		Base64 string `json:"base64"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &reply); err != nil {
		t.Fatalf("download reply %q: %v", res.Content[0].Text, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(reply.Base64)
	if err != nil {
		t.Fatalf("reply base64: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Errorf("round trip content mismatch: got %v, want %v", decoded, payload)
	}
	if reply.Path != "/tmp/shot.png" || reply.Size != len(payload) {
		t.Errorf("reply metadata = path %q size %d, want path /tmp/shot.png size %d", reply.Path, reply.Size, len(payload))
	}

	// One byte over the cap is rejected before reaching the container.
	args, _ = json.Marshal(uploadArgs{
		Path:          "/tmp/big.bin",
		ContentBase64: base64.StdEncoding.EncodeToString(make([]byte, maxBinaryFileBytes+1)),
	})
	res, err = handleUploadFile(ctx, dc, "c1", args)
	if err != nil || !res.IsError {
		t.Fatalf("oversize upload: err=%v isError=%v, want an error result", err, res.IsError)
	}
	if !strings.Contains(res.Content[0].Text, "exceeds") {
		t.Errorf("oversize error = %q, want the size-limit message", res.Content[0].Text)
	}
	if _, ok := fake.files["/tmp/big.bin"]; ok {
		t.Errorf("oversize payload was written to the container anyway")
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
	fileName := ""
	fileBody := ""
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
		} else if hdr.Typeflag == tar.TypeReg {
			fileCount++
			fileName = hdr.Name
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			fileBody = string(body)
		}
	}
	if dirCount != 0 {
		t.Errorf("expected 0 dirs for top-level file, got %d", dirCount)
	}
	if fileCount != 1 {
		t.Errorf("expected 1 file entry, got %d", fileCount)
	}
	if fileName != "app.py" {
		t.Errorf("file name = %q, want app.py", fileName)
	}
	if fileBody != "print(1)" {
		t.Errorf("file body = %q, want print(1)", fileBody)
	}
}

func TestBuildPathTar_EntryNamesRelativeToRoot(t *testing.T) {
	// Entries must be relative (no leading slash) so CopyToContainer at "/"
	// places them correctly.
	out := buildPathTar("/x/y/z", []byte("k"))
	tr := tar.NewReader(bytes.NewReader(out))
	var dirs []string
	var fileName string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasPrefix(hdr.Name, "/") {
			t.Errorf("entry name %q has leading slash — must be relative", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			dirs = append(dirs, hdr.Name)
		} else if hdr.Typeflag == tar.TypeReg {
			fileName = hdr.Name
		}
	}
	// /x/y/z → ancestors "x" and "x/y" plus the file itself at "x/y/z".
	wantDirs := map[string]bool{"x/": true, "x/y/": true}
	if len(dirs) != len(wantDirs) {
		t.Fatalf("expected ancestor dirs %v, got %v", wantDirs, dirs)
	}
	for _, d := range dirs {
		if !wantDirs[d] {
			t.Errorf("unexpected dir entry %q", d)
		}
	}
	if fileName != "x/y/z" {
		t.Errorf("file name = %q, want x/y/z", fileName)
	}
}
