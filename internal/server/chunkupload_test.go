package server

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateFor builds a fresh resume state file under dst with all chunks marked
// received (so complete can assemble), unless skip is set.
func writeFullState(t *testing.T, dst string, size, chunkSize int64, total int) {
	t.Helper()
	st := &chunkUploadState{
		Size:        size,
		ChunkSize:   chunkSize,
		TotalChunks: total,
		Received:    map[int]bool{},
	}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}
}

// segmentsFor writes N segments of `data` split into total chunks, each MD5'd.
func writeSegments(t *testing.T, dst string, data []byte, chunkSize int64, total int) {
	t.Helper()
	for i := 0; i < total; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		seg := data[start:end]
		want := md5.Sum(seg)
		if _, err := writeChunkSegment(dst, i, strings.NewReader(string(seg)), hex.EncodeToString(want[:])); err != nil {
			t.Fatalf("writeChunkSegment %d: %v", i, err)
		}
	}
}

func TestChunkSegment_WritesAndVerifies(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "big.bin")
	data := []byte("0123456789ABCDEF") // 16 bytes
	want := md5.Sum(data)
	if _, err := writeChunkSegment(dst, 0, strings.NewReader(string(data)), hex.EncodeToString(want[:])); err != nil {
		t.Fatalf("writeChunkSegment: %v", err)
	}
	got, err := os.ReadFile(chunkSegmentPath(dst, 0))
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("segment content mismatch")
	}
}

func TestChunkSegment_MismatchRemovesSegment(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "bad.bin")
	data := []byte("corrupt me")
	if _, err := writeChunkSegment(dst, 0, strings.NewReader(string(data)), "00000000000000000000000000000000"); err == nil {
		t.Fatal("expected checksum error, got nil")
	}
	if _, err := os.Stat(chunkSegmentPath(dst, 0)); !os.IsNotExist(err) {
		t.Fatalf("bad segment should be removed, stat=%v", err)
	}
}

func TestAssembleChunks_VerifiesAndCleans(t *testing.T) {
	// A 40-byte "file" in 5 chunks of 8 bytes each.
	data := []byte("AAAABBBBCCCCDDDDEEEEFFFF") // 24 bytes -> 3 chunks of 8
	const chunkSize = 8
	total := 3
	dst := filepath.Join(t.TempDir(), "assembled.bin")

	writeFullState(t, dst, int64(len(data)), chunkSize, total)
	writeSegments(t, dst, data, chunkSize, total)
	// mark all received
	st, err := readChunkState(dst)
	if err != nil {
		t.Fatalf("readChunkState: %v", err)
	}
	for i := 0; i < total; i++ {
		st.Received[i] = true
	}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}

	want := md5.Sum(data)
	sum, err := assembleChunks(dst, st, hex.EncodeToString(want[:]))
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("assembled md5 = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read assembled: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("assembled content mismatch")
	}
	// Artifacts cleaned up on success.
	if _, err := os.Stat(chunkStatePath(dst)); !os.IsNotExist(err) {
		t.Fatalf("state file should be removed after complete")
	}
	for i := 0; i < total; i++ {
		if _, err := os.Stat(chunkSegmentPath(dst, i)); !os.IsNotExist(err) {
			t.Fatalf("segment %d should be removed after complete", i)
		}
	}
}

func TestAssembleChunks_MissingChunkReported(t *testing.T) {
	data := []byte("AAAABBBBCCCC") // 12 bytes, 8-byte chunks -> 2 chunks
	dst := filepath.Join(t.TempDir(), "partial.bin")
	writeFullState(t, dst, int64(len(data)), 8, 2)
	writeSegments(t, dst, data, 8, 2)
	st, _ := readChunkState(dst)
	st.Received[0] = true // chunk 1 NOT received
	_ = writeChunkState(dst, st)

	missing := missingChunks(st)
	if len(missing) != 1 || missing[0] != 1 {
		t.Fatalf("missingChunks = %v, want [1]", missing)
	}
}

func TestAssembleChunks_BadFileMD5Removed(t *testing.T) {
	data := []byte("AAAABBBBCCCC")
	dst := filepath.Join(t.TempDir(), "verify.bin")
	writeFullState(t, dst, int64(len(data)), 8, 2)
	writeSegments(t, dst, data, 8, 2)
	st, _ := readChunkState(dst)
	for i := 0; i < 2; i++ {
		st.Received[i] = true
	}
	_ = writeChunkState(dst, st)
	// Wrong whole-file MD5: assembly must refuse and remove the destination.
	_, err := assembleChunks(dst, st, "00000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, e := os.Stat(dst); !os.IsNotExist(e) {
		t.Fatalf("bad destination should be removed, stat=%v", e)
	}
}

func TestResume_StatePersistsReceived(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "resume.bin")
	st := &chunkUploadState{Size: 100, ChunkSize: 10, TotalChunks: 10, Received: map[int]bool{0: true, 3: true}}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}
	got, err := readChunkState(dst)
	if err != nil {
		t.Fatalf("readChunkState: %v", err)
	}
	if !got.Received[0] || !got.Received[3] || got.Received[1] {
		t.Fatalf("received set not persisted: %+v", got.Received)
	}
	if list := receivedList(got); len(list) != 2 || list[0] != 0 || list[1] != 3 {
		t.Fatalf("receivedList = %v, want [0 3]", list)
	}
}

func TestChunkByteRange_LastChunkShort(t *testing.T) {
	st := &chunkUploadState{Size: 25, ChunkSize: 10, TotalChunks: 3}
	cases := []struct{ idx, start, end int }{
		{0, 0, 10}, {1, 10, 20}, {2, 20, 25},
	}
	for _, c := range cases {
		s, e := chunkByteRange(st, c.idx)
		if int(s) != c.start || int(e) != c.end {
			t.Fatalf("chunk %d range = (%d,%d), want (%d,%d)", c.idx, s, e, c.start, c.end)
		}
	}
}

// TestHandleChunkComplete_ReadsJSONBody is a regression test: the client posts
// {name,uploadId,fileMD5} as a JSON body (Content-Type: application/json), not
// a form-urlencoded or multipart body. r.FormValue never parses a JSON body, so
// reading these fields that way (the original implementation) left name and
// uploadId permanently empty and every completion 400'd with "name and
// uploadId are required" — the large-file upload could never finish.
func TestHandleChunkComplete_ReadsJSONBody(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "big.bin")
	data := []byte("AAAABBBBCCCCDDDDEEEEFFFF") // 24 bytes -> 3 chunks of 8
	const chunkSize = 8
	total := 3

	writeFullState(t, dst, int64(len(data)), chunkSize, total)
	writeSegments(t, dst, data, chunkSize, total)
	st, err := readChunkState(dst)
	if err != nil {
		t.Fatalf("readChunkState: %v", err)
	}
	for i := 0; i < total; i++ {
		st.Received[i] = true
	}
	if err := writeChunkState(dst, st); err != nil {
		t.Fatalf("writeChunkState: %v", err)
	}

	want := md5.Sum(data)
	body, _ := json.Marshal(map[string]string{
		"name":     "big.bin",
		"uploadId": encodeUploadID(dir, "big.bin"),
		"fileMD5":  hex.EncodeToString(want[:]),
	})
	req := httptest.NewRequest(http.MethodPost, "/chunk/complete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleChunkComplete(w, req, dir)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("response ok = %v, want true: %v", resp["ok"], resp)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read assembled file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("assembled content mismatch")
	}
}

// TestHandleChunkAbort_ReadsJSONBody mirrors the complete-side regression: abort
// also takes a JSON body, and must clean up the in-progress artifacts rather
// than 400 on empty name/uploadId.
func TestHandleChunkAbort_ReadsJSONBody(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "partial.bin")
	writeFullState(t, dst, 24, 8, 3)
	if _, err := writeChunkSegment(dst, 0, strings.NewReader("AAAABBBB"), ""); err != nil {
		t.Fatalf("writeChunkSegment: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"name":     "partial.bin",
		"uploadId": encodeUploadID(dir, "partial.bin"),
	})
	req := httptest.NewRequest(http.MethodPost, "/chunk/abort", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleChunkAbort(w, req, dir)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(chunkStatePath(dst)); !os.IsNotExist(err) {
		t.Fatalf("state file should be removed after abort, stat=%v", err)
	}
	if _, err := os.Stat(chunkSegmentPath(dst, 0)); !os.IsNotExist(err) {
		t.Fatalf("segment should be removed after abort, stat=%v", err)
	}
}

func TestEqualFoldHex(t *testing.T) {
	if !equalFoldHex("ABCDEF0123456789abcdef0123456789", "abcdef0123456789ABCDEF0123456789") {
		t.Fatal("case-insensitive hex compare failed")
	}
	if equalFoldHex("abc", "abcd") {
		t.Fatal("different-length digests should not be equal")
	}
}
