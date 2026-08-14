package server

// Security-audit regression tests backing docs/SECURITY-AUDIT.md.
//
// Two kinds of tests live here, following the convention established by
// security_regression_test.go:
//
//   - Positive regressions for protections that exist in the code but had NO
//     test coverage before this audit (tar extraction traversal guard, zip
//     symlink skipping, PowerShell/shell quoting, displayName validation).
//     These must keep passing.
//
//   - Fix-verification tests for the issues the audit found and that have
//     since been FIXED (chunk-upload layout/quota hardening, opaque upload
//     ids, tar member matching, zip member containment, logout CSRF,
//     proxy-gated HSTS). Each names the audit item it closes; if one of these
//     ever fails, the corresponding hardening was regressed.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tarEntry describes one entry for buildTar.
type tarEntry struct {
	name     string
	typeflag byte
	content  string
	linkname string
}

// buildTar serializes entries into an in-memory tar stream, so tests can feed
// hand-crafted (hostile) archives to the extraction/streaming helpers exactly
// the way a compromised container would emit them via the Docker archive API.
func buildTar(t *testing.T, entries []tarEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Mode:     0o644,
			Size:     int64(len(e.content)),
		}
		switch e.typeflag {
		case tar.TypeDir:
			hdr.Mode = 0o755
			hdr.Size = 0
			if !strings.HasSuffix(hdr.Name, "/") {
				hdr.Name += "/"
			}
		case tar.TypeSymlink, tar.TypeLink:
			hdr.Linkname = e.linkname
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if e.content != "" {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("write content %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return &buf
}

// ===========================================================================
// A. extractContainerTar — traversal / symlink / bomb hardening
//    (container_files.go:341; positive regressions, previously untested)
// ===========================================================================

// TestAuditExtractContainerTarRejectsTraversal feeds a hostile container tar
// carrying the classic ZipSlip/Slip names and asserts nothing lands outside
// the destination directory.
func TestAuditExtractContainerTarRejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	outside := filepath.Dir(dest) // the parent the payloads aim at

	arc := buildTar(t, []tarEntry{
		{name: "../escape-parent.txt", typeflag: tar.TypeReg, content: "pwn"},
		{name: "sub/../../escape-nested.txt", typeflag: tar.TypeReg, content: "pwn"},
		{name: "/absolute-escape.txt", typeflag: tar.TypeReg, content: "pwn"},
		{name: "ok.txt", typeflag: tar.TypeReg, content: "fine"},
		{name: "sub/inner.txt", typeflag: tar.TypeReg, content: "fine too"},
	})

	n, err := extractContainerTar(arc, dest)
	if err != nil {
		t.Fatalf("extractContainerTar: %v", err)
	}
	// The two contained entries plus /absolute-escape.txt, whose leading slash
	// is stripped so it lands INSIDE dest (neutralized, not escaped). The two
	// ".."-carrying payloads must be skipped entirely.
	if n != 3 {
		t.Fatalf("written = %d, want 3 (traversal entries must be skipped)", n)
	}
	for _, name := range []string{"escape-parent.txt", "escape-nested.txt"} {
		if _, err := os.Stat(filepath.Join(outside, name)); err == nil {
			t.Errorf("traversal payload %q escaped the destination dir", name)
		}
		if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
			t.Errorf("traversal payload %q was written inside dest instead of being skipped", name)
		}
	}
	// An absolute name is clamped to dest, never resolved against the host root.
	if b, err := os.ReadFile(filepath.Join(dest, "absolute-escape.txt")); err != nil || string(b) != "pwn" {
		t.Errorf("absolute path was not clamped inside dest: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(string(filepath.Separator), "absolute-escape.txt")); err == nil {
		t.Errorf("absolute payload escaped to the host root")
	}
	if b, err := os.ReadFile(filepath.Join(dest, "ok.txt")); err != nil || string(b) != "fine" {
		t.Errorf("ok.txt not extracted intact: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "sub", "inner.txt")); err != nil || string(b) != "fine too" {
		t.Errorf("sub/inner.txt not extracted intact: %q, %v", b, err)
	}
}

// TestAuditExtractContainerTarSkipsSymlinks pins the defense that keeps a
// malicious container from planting links pointing outside the netdisk root
// (the follow-up read through such a link is the actual escalation).
func TestAuditExtractContainerTarSkipsSymlinks(t *testing.T) {
	dest := t.TempDir()
	arc := buildTar(t, []tarEntry{
		{name: "hostkeys", typeflag: tar.TypeSymlink, linkname: "/root/.ssh"},
		{name: "hardlink", typeflag: tar.TypeLink, linkname: "/etc/passwd"},
		{name: "real.txt", typeflag: tar.TypeReg, content: "kept"},
	})

	if _, err := extractContainerTar(arc, dest); err != nil {
		t.Fatalf("extractContainerTar: %v", err)
	}
	for _, name := range []string{"hostkeys", "hardlink"} {
		if _, err := os.Lstat(filepath.Join(dest, name)); err == nil {
			t.Errorf("link entry %q must be skipped, not extracted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "real.txt")); err != nil {
		t.Errorf("regular entry lost: %v", err)
	}
}

// TestAuditExtractContainerTarMalformedSizeSkipped: a negative hdr.Size is
// impossible for well-formed archives; the extractor must skip the entry
// rather than hand a negative length to io.LimitReader.
func TestAuditExtractContainerTarMalformedSizeSkipped(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "neg.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: -1})
	_ = tw.WriteHeader(&tar.Header{Name: "fine.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4})
	_, _ = tw.Write([]byte("fine"))
	_ = tw.Close()

	n, err := extractContainerTar(&buf, dest)
	if err != nil {
		t.Fatalf("extractContainerTar: %v", err)
	}
	if n != 1 {
		t.Fatalf("written = %d, want 1 (negative-size entry skipped)", n)
	}
}

// ===========================================================================
// B. streamContainerTarAsZip / streamTarFile (container download path)
// ===========================================================================

// readZipNames parses a zip from a buffer, returning its entry names.
func readZipNames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// TestAuditStreamContainerTarAsZipSkipsSymlinks is a positive regression: the
// on-the-fly re-archiver must never emit a zip entry for a symlink/hardlink
// tar member (the link target would be followed at extraction time).
func TestAuditStreamContainerTarAsZipSkipsSymlinks(t *testing.T) {
	arc := buildTar(t, []tarEntry{
		{name: "dir/", typeflag: tar.TypeDir},
		{name: "dir/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		{name: "dir/hard", typeflag: tar.TypeLink, linkname: "/etc/shadow"},
		{name: "dir/file.txt", typeflag: tar.TypeReg, content: "kept"},
	})
	var out bytes.Buffer
	streamContainerTarAsZip(&out, arc)

	names := readZipNames(t, &out)
	for _, n := range names {
		if strings.HasSuffix(n, "link") || strings.HasSuffix(n, "hard") {
			t.Errorf("zip contains link entry %q — must be skipped", n)
		}
	}
	found := false
	for _, n := range names {
		if strings.HasSuffix(n, "file.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("regular entry lost from zip; entries = %v", names)
	}
}

// TestAuditStreamContainerTarAsZipRejectsTraversalNames backs the fix for
// docs/SECURITY-AUDIT.md L-3: tar member names are confined to the archive
// root before becoming zip entries, so a hostile container cannot ship a zip
// whose members escape the extraction root on the DOWNLOADING USER's machine
// (client-side ZipSlip).
func TestAuditStreamContainerTarAsZipRejectsTraversalNames(t *testing.T) {
	arc := buildTar(t, []tarEntry{
		{name: "../evil.txt", typeflag: tar.TypeReg, content: "slip"},
		{name: "a/../../evil2.txt", typeflag: tar.TypeReg, content: "slip"},
		{name: "ok.txt", typeflag: tar.TypeReg, content: "fine"},
		{name: "dir/ok2.txt", typeflag: tar.TypeReg, content: "fine too"},
	})
	var out bytes.Buffer
	streamContainerTarAsZip(&out, arc)

	for _, n := range readZipNames(t, &out) {
		cleaned := path.Clean(n)
		if cleaned == ".." || cleaned == "." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
			t.Errorf("zip entry %q escapes the archive root", n)
		}
	}
	names := readZipNames(t, &out)
	for _, want := range []string{"ok.txt", "dir/ok2.txt"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("contained entry %q lost from zip; entries = %v", want, names)
		}
	}
	if len(names) != 2 {
		t.Errorf("traversal entries must be skipped entirely; entries = %v", names)
	}
}

// TestAuditStreamTarFileServesOnlyNamedEntry backs the fix for
// docs/SECURITY-AUDIT.md L-2: the member match is on the entry NAME (plus
// TypeReg), never on "it's a regular file" — a crafted archive whose first
// regular entry is a decoy must not have the decoy's bytes served under the
// requested file's name.
func TestAuditStreamTarFileServesOnlyNamedEntry(t *testing.T) {
	arc := buildTar(t, []tarEntry{
		{name: "decoy.txt", typeflag: tar.TypeReg, content: "ATTACKER BYTES"},
		{name: "target.txt", typeflag: tar.TypeReg, content: "real bytes"},
	})
	var out bytes.Buffer
	if err := streamTarFile(&out, arc, "target.txt"); err != nil {
		t.Fatalf("streamTarFile: %v", err)
	}
	if out.String() != "real bytes" {
		t.Fatalf("streamTarFile served %q, want the named member's bytes only", out.String())
	}

	// No member matches at all: the stream must end without emitting bytes.
	var none bytes.Buffer
	arc2 := buildTar(t, []tarEntry{
		{name: "decoy.txt", typeflag: tar.TypeReg, content: "ATTACKER BYTES"},
	})
	if err := streamTarFile(&none, arc2, "target.txt"); err != io.EOF {
		t.Fatalf("streamTarFile for absent member: err = %v, want io.EOF", err)
	}
	if none.Len() != 0 {
		t.Fatalf("bytes served for a non-matching member: %q", none.String())
	}
}

// ===========================================================================
// C. Windows shortcut creation — PowerShell quoting (symlink.go)
//    Positive regressions, previously untested.
// ===========================================================================

// TestAuditEscapePathForPowerShell verifies the single-quote literal escaping
// that keeps Feishu-sourced display names (attacker-influenced) from breaking
// out of the PowerShell script that creates .lnk shortcuts. Every payload
// must end up as one fully single-quoted literal: interpolation ($(), “ ` “)
// is inert inside single quotes, and embedded quotes are doubled (”), which
// PowerShell reads as a literal quote.
func TestAuditEscapePathForPowerShell(t *testing.T) {
	payloads := []string{
		`C:\netdisk\a'b`,
		`'; $(Remove-Item -Recurse C:\) ; '`,
		"a`$(calc.exe)b",
		`" | calc.exe #`,
		`x'; Start-Process calc; 'x`,
	}
	for _, p := range payloads {
		got := escapePathForPowerShell(p)
		if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
			t.Fatalf("payload %q: %q is not a single-quoted literal", p, got)
		}
		inner := got[1 : len(got)-1]
		// Every remaining quote must be part of a '' pair (escaped), never a
		// lone terminator that would close the literal early.
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\'' {
				if i+1 >= len(inner) || inner[i+1] != '\'' {
					t.Fatalf("payload %q: unescaped quote in %q at %d", p, got, i)
				}
				i++
			}
		}
		// Round-trip sanity: doubling every '' back to ' recovers the input.
		if strings.ReplaceAll(inner, "''", "'") != p {
			t.Fatalf("payload %q does not round-trip through %q", p, got)
		}
	}
}

// TestAuditCreateSymlinkRejectsTraversalNames pins the upstream validation the
// PowerShell comment block relies on (symlink.go:110-113): a displayName from
// an OAuth profile may not contain path separators or be a dot segment.
func TestAuditCreateSymlinkRejectsTraversalNames(t *testing.T) {
	for _, name := range []string{"a/b", `a\b`, "..", ".", `..\..\evil`} {
		err := CreateSymlink(t.TempDir(), t.TempDir(), name)
		if err == nil {
			t.Errorf("displayName %q accepted; must be rejected", name)
		} else if !strings.Contains(err.Error(), "invalid characters") {
			t.Errorf("displayName %q: unexpected error %v", name, err)
		}
	}
}

// ===========================================================================
// D. Chunked upload — quota consistency, DoS bound, uploadId disclosure
//    (chunkupload.go; KNOWN GAP pins from docs/SECURITY-AUDIT.md M-1..M-3)
// ===========================================================================

// initChunkResp runs handleChunkInit against a temp dir and decodes its JSON
// response, so each gap test only asserts on the behavior it pins.
func initChunkResp(t *testing.T, dir, name string, req chunkInitReq, quota func(add int64) error) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	handleChunkInit(w, dir, name, req, quota)
	var out map[string]any
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode init response: %v (%s)", err, w.Body.String())
		}
	}
	return w.Code, out
}

// TestAuditChunkInitRejectsInconsistentLayout backs the fix for
// docs/SECURITY-AUDIT.md M-1: init refuses size/chunkSize/totalChunks triples
// that are not arithmetically consistent (totalChunks != ceil(size/chunkSize)),
// so the quota projection can never be scoped to a tiny declared size while
// arbitrarily many chunks follow.
func TestAuditChunkInitRejectsInconsistentLayout(t *testing.T) {
	dir := t.TempDir()

	t.Run("impossible layout rejected", func(t *testing.T) {
		var quotaSaw int64
		code, _ := initChunkResp(t, dir, "lie.bin", chunkInitReq{
			Size: 1 << 20, ChunkSize: 8, TotalChunks: 1_000_000, // ceil(1MiB/8) = 131072, not 1e6
		}, func(add int64) error { quotaSaw = add; return nil })
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for the inconsistent layout", code)
		}
		if quotaSaw != 0 {
			t.Fatalf("quota projection ran (%d) despite the rejected layout", quotaSaw)
		}
	})

	t.Run("chunkSize above the request-body cap rejected", func(t *testing.T) {
		code, _ := initChunkResp(t, dir, "huge-chunks.bin", chunkInitReq{
			Size: 320 << 20, ChunkSize: 320 << 20, TotalChunks: 1,
		}, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for chunkSize above the body cap", code)
		}
	})

	t.Run("zero-size file rejected (ceil(0/x) != totalChunks)", func(t *testing.T) {
		code, _ := initChunkResp(t, dir, "empty.bin", chunkInitReq{Size: 0, ChunkSize: 8, TotalChunks: 1}, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for the zero-size layout", code)
		}
	})

	t.Run("valid layout still accepted", func(t *testing.T) {
		code, _ := initChunkResp(t, dir, "honest.bin", chunkInitReq{
			Size: 24, ChunkSize: 8, TotalChunks: 3,
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("valid layout rejected: status = %d", code)
		}
	})
}

// TestAuditChunkInitCapsTotalChunks backs the fix for docs/SECURITY-AUDIT.md
// M-2: TotalChunks has an upper bound, so missingChunks/removeChunkArtifacts
// (both O(totalChunks)) can never be turned into a huge allocation or a
// million-syscall loop.
func TestAuditChunkInitCapsTotalChunks(t *testing.T) {
	dir := t.TempDir()
	code, out := initChunkResp(t, dir, "bomb.bin", chunkInitReq{
		Size: 1, ChunkSize: 1, TotalChunks: 100_000_001,
	}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for totalChunks above the cap", code)
	}
	if out != nil {
		t.Fatalf("no success payload expected, got %v", out)
	}
	// And no state file may exist for the rejected init — there is nothing to
	// resume or abort later.
	dst := filepath.Join(dir, "bomb.bin")
	if _, err := os.Stat(chunkStatePath(dst)); !os.IsNotExist(err) {
		t.Fatalf("state file must not be created for a rejected init, stat=%v", err)
	}

	// A pre-hardening bomb state on disk (hand-written, bypassing init) is
	// refused at read time too, so complete/abort stay bounded.
	st := &chunkUploadState{Size: 1, ChunkSize: 1, TotalChunks: maxUploadChunks + 1, UploadID: testUploadID, Received: map[int]bool{}}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}
	if _, err := readChunkState(dst); err == nil {
		t.Fatal("readChunkState accepted a state exceeding the chunk cap")
	}
}

// TestAuditChunkSegmentEnforcesDeclaredLength backs the fix for
// docs/SECURITY-AUDIT.md M-1: a chunk body must be exactly its declared
// chunkByteRange length — short (truncated) and oversized (quota-smuggling)
// bodies are rejected and the segment removed.
func TestAuditChunkSegmentEnforcesDeclaredLength(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "over.bin")

	t.Run("oversized segment rejected", func(t *testing.T) {
		big := bytes.Repeat([]byte("A"), 1<<20) // 1 MiB into an 8-byte slot
		_, err := writeChunkSegment(dst, 0, bytes.NewReader(big), "", 8)
		if err == nil {
			t.Fatal("oversized segment accepted")
		}
		if _, serr := os.Stat(chunkSegmentPath(dst, 0)); !os.IsNotExist(serr) {
			t.Fatalf("rejected segment must be removed, stat=%v", serr)
		}
	})

	t.Run("short segment rejected", func(t *testing.T) {
		if _, err := writeChunkSegment(dst, 1, strings.NewReader("abc"), "", 8); err == nil {
			t.Fatal("short segment accepted")
		}
		if _, serr := os.Stat(chunkSegmentPath(dst, 1)); !os.IsNotExist(serr) {
			t.Fatalf("rejected segment must be removed, stat=%v", serr)
		}
	})

	t.Run("exact segment accepted", func(t *testing.T) {
		if _, err := writeChunkSegment(dst, 2, strings.NewReader("12345678"), "", 8); err != nil {
			t.Fatalf("exact segment rejected: %v", err)
		}
	})
}

// TestAuditAssembleChunksRejectsSizeMismatch is the definitive M-1 closure:
// even if some path ever slipped wrong-sized segments past the write-time pin,
// assembly compares the concatenated byte count against the declared Size and
// refuses to keep the file. (The whole-file CRC the shipped frontend leaves
// empty cannot be relied on — see web/lib/chunkupload.js fileCRC32: "".)
func TestAuditAssembleChunksRejectsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "lie.bin")
	// Declares 12 bytes as 2x8 chunks... with segments that total more.
	writeFullState(t, dst, 12, 8, 2)
	if _, err := writeChunkSegment(dst, 0, strings.NewReader("12345678"), "", 8); err != nil {
		t.Fatalf("writeChunkSegment 0: %v", err)
	}
	if _, err := writeChunkSegment(dst, 1, strings.NewReader("AAAABBBBCCCC"), "", 12); err != nil {
		t.Fatalf("writeChunkSegment 1: %v", err)
	}
	st, err := readChunkState(dst)
	if err != nil {
		t.Fatalf("readChunkState: %v", err)
	}
	st.Received[0] = true
	st.Received[1] = true
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}
	if _, err := assembleChunks(dst, st, ""); err == nil {
		t.Fatal("assembly accepted a size that contradicts the declared layout")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatalf("contradicting destination must be removed, stat=%v", serr)
	}
}

// TestAuditChunkInitUploadIDIsOpaque backs the fix for docs/SECURITY-AUDIT.md
// M-3: the uploadId handed to the client is a random hex handle that encodes
// nothing about the server's filesystem — the earlier scheme returned the
// resolved absolute path (netdisk layout / Docker volume mountpoints).
func TestAuditChunkInitUploadIDIsOpaque(t *testing.T) {
	dir := t.TempDir()
	code, out := initChunkResp(t, dir, "secret.bin", chunkInitReq{Size: 8, ChunkSize: 8, TotalChunks: 1}, nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	id, _ := out["uploadId"].(string)
	if id == "" {
		t.Fatal("uploadId missing from init response")
	}
	// Pure hex, 32 chars (16 random bytes): no separators, no path fragments.
	if len(id) != 32 {
		t.Fatalf("uploadId = %q, want 32 hex chars", id)
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("uploadId %q is not lowercase hex", id)
		}
	}
	// And it must not disclose any part of the destination's absolute path.
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if id == dirAbs || strings.Contains(id, "secret.bin") || filepath.IsAbs(id) {
		t.Fatalf("uploadId %q discloses the host path %q", id, dirAbs)
	}
	// Two inits for different names never produce colliding handles.
	_, out2 := initChunkResp(t, dir, "other.bin", chunkInitReq{Size: 8, ChunkSize: 8, TotalChunks: 1}, nil)
	if id2, _ := out2["uploadId"].(string); id2 == id {
		t.Fatalf("two uploads share the handle %q", id)
	}
}

// ===========================================================================
// E. Router-level gaps (full middleware chain, real routes)
// ===========================================================================

// TestAuditLogoutRequiresCSRF backs the fix for docs/SECURITY-AUDIT.md L-1:
// /api/logout lives inside the auth+CSRF group, so a cross-site form riding
// the ambient session cookie cannot force a victim's logout. The request
// without the CSRF header must be refused AND the session must stay intact.
func TestAuditLogoutRequiresCSRF(t *testing.T) {
	_, _, user := newSecurityTestServer(t)
	resp, body := user.postJSONNoCSRF("/api/logout", map[string]string{})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without the CSRF header; body=%s", resp.StatusCode, body)
	}
	// The session must have survived the refused logout. When authenticated,
	// /api/me returns the full user row (no "authenticated" key — that shape
	// only appears for anonymous requests); the username is the stable proof.
	resp, body = user.get("/api/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/me after refused logout: status = %d body=%s", resp.StatusCode, body)
	}
	var me struct {
		Username      string `json:"username"`
		Authenticated bool   `json:"authenticated"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode /api/me: %v (%s)", err, body)
	}
	if me.Username != "secuser" {
		t.Fatalf("session was cleared despite the refused logout (%s)", body)
	}

	// With the header, logout works as before.
	resp, body = user.postJSON("/api/logout", map[string]string{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout with CSRF: status = %d body=%s", resp.StatusCode, body)
	}
}

// TestAuditXForwardedProtoIgnoredFromUntrustedPeer backs the fix for
// docs/SECURITY-AUDIT.md L-5: X-Forwarded-Proto is only believed when the
// request arrived from a configured trusted proxy, matching the rate limiter's
// ClientIP discipline. A direct plain-HTTP client forging the header must not
// earn HSTS on a plaintext response (which would pin a host that may not
// serve HTTPS yet).
func TestAuditXForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	baseURL, _, _ := newSecurityTestServer(t) // httptest = plain HTTP, no trusted proxies

	req, err := http.NewRequest(http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q on a spoofed X-Forwarded-Proto over plain HTTP; want absent", got)
	}
}
