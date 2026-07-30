package server

import (
	"bytes"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// makeFileHeader builds a real *multipart.FileHeader for `content` named `name`,
// using the same parser the upload handlers run. This exercises the seekable
// multipart.File that writeFileWithCRC32 relies on for resume.
func makeFileHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("files", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req, err := http.NewRequest("POST", "/upload", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse: %v", err)
	}
	files := req.MultipartForm.File["files"]
	if len(files) != 1 {
		t.Fatalf("expected 1 file part, got %d", len(files))
	}
	return files[0]
}

// crc32Hex returns the lowercase hex CRC32 (IEEE) of content, matching what
// writeFileWithCRC32 computes.
func crc32Hex(content []byte) string {
	sum := crc32.ChecksumIEEE(content)
	b := []byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
	return hex.EncodeToString(b)
}

func TestWriteFileWithCRC32_CorrectContent(t *testing.T) {
	content := []byte("the quick brown fox jumps over the lazy dog")
	wantHex := crc32Hex(content)

	fh := makeFileHeader(t, "fox.txt", content)
	src, err := fh.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	dst := filepath.Join(t.TempDir(), "fox.txt")
	got, err := writeFileWithCRC32(dst, src, fh, wantHex)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got != wantHex {
		t.Fatalf("returned crc32 = %s, want %s", got, wantHex)
	}
	// The file on disk must match the digest the server computed.
	written, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(written, content) {
		t.Fatalf("written content mismatch")
	}
}

func TestWriteFileWithCRC32_MismatchRemovesFile(t *testing.T) {
	content := []byte("good content")
	fh := makeFileHeader(t, "bad.txt", content)
	src, err := fh.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	dst := filepath.Join(t.TempDir(), "bad.txt")
	// A deliberately wrong expected hash must be rejected, and the partially/
	// fully-written file must be removed so a later resume can't trust it.
	got, err := writeFileWithCRC32(dst, src, fh, "00000000")
	if err == nil || !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got err=%v crc32=%s", err, got)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt file should be removed, stat err=%v", statErr)
	}
}

func TestWriteFileWithCRC32_EmptyExpectedStillWrites(t *testing.T) {
	// Legacy clients / Worker-unavailable browsers send no hash: the file must
	// still be written and its real CRC32 returned (server-side verification path).
	content := []byte("no client hash")
	wantHex := crc32Hex(content)

	fh := makeFileHeader(t, "plain.bin", content)
	src, err := fh.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	dst := filepath.Join(t.TempDir(), "plain.bin")
	got, err := writeFileWithCRC32(dst, src, fh, "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got != wantHex {
		t.Fatalf("returned crc32 = %s, want %s", got, wantHex)
	}
}

// TestWriteFileWithCRC32_OverwritesSmallerExistingFile is a regression test for a
// legacy "resume" optimization that seeked into a pre-existing same-named file
// and appended only the newly-read bytes past its length. A browser always
// resends a multipart part in full from byte 0 (it cannot resume a plain file
// upload mid-stream), so that offset was never real resumption — it just
// spliced the new upload onto whatever unrelated bytes happened to already be
// on disk (e.g. an older, smaller file with the same name), silently
// corrupting the result. writeFileWithCRC32 must always start from byte 0.
func TestWriteFileWithCRC32_OverwritesSmallerExistingFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "same-name.bin")
	if err := os.WriteFile(dst, []byte("OLD-UNRELATED-CONTENT"), 0o640); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	content := []byte("brand new content")
	wantHex := crc32Hex(content)
	fh := makeFileHeader(t, "same-name.bin", content)
	src, err := fh.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	got, err := writeFileWithCRC32(dst, src, fh, wantHex)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got != wantHex {
		t.Fatalf("returned crc32 = %s, want %s", got, wantHex)
	}
	written, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(written, content) {
		t.Fatalf("written content = %q, want %q (old bytes must not survive)", written, content)
	}
}

func TestCountFailedResults(t *testing.T) {
	rs := []uploadResult{
		{Path: "a", CRC32: "00"},
		{Path: "b", Error: "boom"},
		{Path: "c", CRC32: "11"},
		{Path: "d", Error: "disk full"},
	}
	if n := countFailedResults(rs); n != 2 {
		t.Fatalf("countFailedResults = %d, want 2", n)
	}
}
