package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Resumable chunked uploads for large files (>= ~1GB). The protocol is
// stateless on the server side apart from two artifacts kept next to the
// destination file:
//
//   <dst>.mudppart          — a small JSON "state" file (chunkUploadState)
//   <dst>.mudppart.<index>  — one segment file per received chunk
//
// This keeps everything on the filesystem (no DB table/migration) and lets an
// interrupted upload resume across process restarts or browser refreshes: the
// client calls init, learns which chunks the server already has, and only sends
// the missing ones. Each chunk is CRC32-verified on write; the assembled file is
// CRC32-verified again on complete. CRC32 (not MD5) because this is a
// corruption check, not a security digest, and it's cheap enough that hashing
// megabytes of chunks stays essentially free.

const mudppartSuffix = ".mudppart" // state file + segment prefix

// chunkUploadState is the on-disk resume record. Stored as JSON at <dst>.mudppart.
type chunkUploadState struct {
	Size        int64        `json:"size"`        // total file size in bytes
	ChunkSize   int64        `json:"chunkSize"`   // bytes per chunk (last may be smaller)
	TotalChunks int          `json:"totalChunks"` // number of chunks
	FileCRC32   string       `json:"fileCRC32"`   // expected whole-file crc32 ("" if unknown)
	Received    map[int]bool `json:"received"`    // index -> received
	// RelPath is the user-relative destination (e.g. "docs/big.bin"). It is the
	// source of truth for where segments and the final file live; the uploadId a
	// client carries is just an opaque encoding of this path.
	RelPath string `json:"relPath" ` //nolint:revive
}

// chunkStateLocks serializes the read-modify-write of one destination's resume
// state across concurrent chunk uploads for that SAME file (the client
// uploads chunks with concurrency > 1 by design). Without this, two goroutines
// can each read the same Received map, mark a different index, and the second
// write silently clobbers the first — the chunk's bytes are on disk, but the
// record that it arrived is lost, which later surfaces as a bogus "missing
// chunks" error on complete. Keyed by destination path; entries are never
// evicted, but each is just a *sync.Mutex, so the steady-state memory cost is
// negligible relative to the uploads themselves.
var chunkStateLocks sync.Map // map[string]*sync.Mutex

// lockChunkState acquires the per-destination lock, returning the unlock func
// the caller must defer.
func lockChunkState(dst string) func() {
	v, _ := chunkStateLocks.LoadOrStore(dst, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// chunkStatePath returns the path of the resume state file for a destination.
func chunkStatePath(dst string) string { return dst + mudppartSuffix }

// chunkSegmentPath returns the path of the segment file for chunk `index`.
func chunkSegmentPath(dst string, index int) string {
	return fmt.Sprintf("%s.%d", chunkStatePath(dst), index)
}

// encodeUploadID turns a user-relative path into the opaque token the client
// carries between init/chunk/complete/abort. It is NOT security — the handlers
// re-resolve and confine the path on every call — it just avoids sending the
// raw path back and forth and gives the client a stable handle.
func encodeUploadID(root, rel string) string {
	return filepath.Join(root, rel) // resolved absolute path serves as the id
}

// readChunkState loads the resume record, returning (nil, os.IsNotExist) when
// there is no in-progress upload for dst.
func readChunkState(dst string) (*chunkUploadState, error) {
	data, err := os.ReadFile(chunkStatePath(dst))
	if err != nil {
		return nil, err
	}
	var st chunkUploadState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if st.Received == nil {
		st.Received = map[int]bool{}
	}
	return &st, nil
}

// writeChunkState atomically persists the resume record (write temp + rename)
// so a crash mid-write cannot corrupt the state file and lose received chunks.
func writeChunkState(dst string, st *chunkUploadState) error {
	if st.Received == nil {
		st.Received = map[int]bool{}
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// handleChunkInit calls this before any segment has been written, so for a
	// brand-new destination folder nothing has created it yet (writeChunkSegment
	// and assembleChunks each MkdirAll their own target, but init runs first).
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	statePath := chunkStatePath(dst)
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath)
}

// receivedList returns the sorted list of received chunk indices for reporting.
func receivedList(st *chunkUploadState) []int {
	out := make([]int, 0, len(st.Received))
	for i := range st.Received {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// missingChunks returns the indices that have NOT been received yet, in order.
func missingChunks(st *chunkUploadState) []int {
	var out []int
	for i := 0; i < st.TotalChunks; i++ {
		if !st.Received[i] {
			out = append(out, i)
		}
	}
	return out
}

// chunkByteRange returns [start,end) for chunk `index` given the file layout.
// The last chunk is short when size is not a multiple of chunkSize.
func chunkByteRange(st *chunkUploadState, index int) (int64, int64) {
	start := int64(index) * st.ChunkSize
	end := start + st.ChunkSize
	if end > st.Size {
		end = st.Size
	}
	return start, end
}

// writeChunkSegment writes one chunk's bytes to its segment file, hashing as it
// goes. expectedCRC32 ("") disables per-chunk verification. The segment is
// always written from offset 0 (a chunk is an independent unit, not appended),
// so a retransmit cleanly replaces a prior bad segment. Returns the computed
// CRC32.
func writeChunkSegment(dst string, index int, src io.Reader, expectedCRC32 string) (string, error) {
	segPath := chunkSegmentPath(dst, index)
	if err := os.MkdirAll(filepath.Dir(segPath), 0o750); err != nil {
		return "", err
	}
	f, err := os.OpenFile(segPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}
	hash := crc32.NewIEEE()
	_, copyErr := io.Copy(io.MultiWriter(f, hash), src)
	_ = f.Close()
	if copyErr != nil {
		_ = os.Remove(segPath)
		return "", copyErr
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if expectedCRC32 != "" && !equalFoldHex(sum, expectedCRC32) {
		_ = os.Remove(segPath)
		return sum, fmt.Errorf("%w: chunk %d expected %s got %s", ErrChecksumMismatch, index, expectedCRC32, sum)
	}
	return sum, nil
}

// assembleChunks concatenates all segment files into dst in order, hashing the
// whole stream so the final file can be verified against expectedFileCRC32. On
// any error the partial destination is removed. All segments + the state file
// are cleaned up on success.
func assembleChunks(dst string, st *chunkUploadState, expectedFileCRC32 string) (string, error) {
	missing := missingChunks(st)
	if len(missing) > 0 {
		return "", fmt.Errorf("missing %d chunk(s): %v", len(missing), missing)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}
	hash := crc32.NewIEEE()
	mw := io.MultiWriter(out, hash)
	for i := 0; i < st.TotalChunks; i++ {
		seg, err := os.Open(chunkSegmentPath(dst, i))
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
			return "", fmt.Errorf("open segment %d: %w", i, err)
		}
		if _, err := io.Copy(mw, seg); err != nil {
			_ = seg.Close()
			_ = out.Close()
			_ = os.Remove(dst)
			return "", fmt.Errorf("copy segment %d: %w", i, err)
		}
		_ = seg.Close()
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if expectedFileCRC32 != "" && !equalFoldHex(sum, expectedFileCRC32) {
		_ = os.Remove(dst)
		return sum, fmt.Errorf("%w: file expected %s got %s", ErrChecksumMismatch, expectedFileCRC32, sum)
	}
	// Success: remove all segments and the state file. The destination stays.
	removeChunkArtifacts(dst, st)
	return sum, nil
}

// removeChunkArtifacts deletes every segment file and the state file. Best-effort.
func removeChunkArtifacts(dst string, st *chunkUploadState) {
	for i := 0; i < st.TotalChunks; i++ {
		_ = os.Remove(chunkSegmentPath(dst, i))
	}
	_ = os.Remove(chunkStatePath(dst))
}

// equalFoldHex compares two hex digests case-insensitively (clients may send
// upper- or lower-case).
func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// ---- Protocol handlers (shared by netdisk + volume) --------------------------
//
// These take an already-resolved, confined destination directory and the
// user-relative name, so the per-surface handlers only differ in how they turn
// the request into (dir, name) and whether they enforce quota. All four write
// JSON responses directly and return nothing.

// chunkInitReq is the JSON body of /chunk/init.
type chunkInitReq struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ChunkSize   int64  `json:"chunkSize"`
	TotalChunks int    `json:"totalChunks"`
	FileCRC32   string `json:"fileCRC32"`
}

// handleChunkInit creates or resumes an upload. `quotaCheck(add)` is invoked
// with the projected additional bytes (size minus any already-received bytes)
// and may return an error to reject the upload for quota/disk reasons.
func handleChunkInit(w http.ResponseWriter, dir, rawName string, req chunkInitReq, quotaCheck func(add int64) error) {
	name := strings.TrimSpace(rawName)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Size < 0 || req.ChunkSize <= 0 || req.TotalChunks <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid size/chunkSize/totalChunks")
		return
	}
	// Resolve the destination within dir and confine it.
	dst, _, err := cleanUserPath(dir, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if dirSelf, _, e := cleanUserPath(dir, ""); e == nil && dst == dirSelf {
		writeErr(w, http.StatusBadRequest, "invalid file name")
		return
	}

	// Resume if a matching state file exists; otherwise start fresh.
	var st *chunkUploadState
	if existing, err := readChunkState(dst); err == nil {
		if existing.Size == req.Size && existing.ChunkSize == req.ChunkSize && existing.TotalChunks == req.TotalChunks {
			st = existing
		}
	}
	alreadyReceived := int64(0)
	if st != nil {
		for i := range st.Received {
			s, e := chunkByteRange(st, i)
			alreadyReceived += e - s
		}
	} else {
		st = &chunkUploadState{
			Size:        req.Size,
			ChunkSize:   req.ChunkSize,
			TotalChunks: req.TotalChunks,
			FileCRC32:   req.FileCRC32,
			Received:    map[int]bool{},
		}
	}
	// Quota/disk projection: only the not-yet-received bytes are new.
	if quotaCheck != nil {
		if err := quotaCheck(req.Size - alreadyReceived); err != nil {
			writeErr(w, http.StatusInsufficientStorage, err.Error())
			return
		}
	}
	if err := writeChunkState(dst, st); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId":    encodeUploadID(dir, name),
		"resume":      alreadyReceived > 0,
		"received":    receivedList(st),
		"chunkSize":   st.ChunkSize,
		"totalChunks": st.TotalChunks,
	})
}

// multipartValue returns the value of a multipart form field, reading only
// r.MultipartForm.Value (the parsed body part). Unlike r.FormValue, this never
// falls back to a same-named URL query parameter — which matters here because
// the volume endpoints put a *different* "name" (the volume identifier) on the
// URL query string (?name=), while the body's "name" field is the in-volume
// file path. r.FormValue merges query + body values under one key and returns
// the query one first, silently substituting the wrong "name" for volume
// uploads (netdisk has no such collision, so it never surfaced there).
func multipartValue(r *http.Request, key string) string {
	if r.MultipartForm == nil {
		return ""
	}
	if vs := r.MultipartForm.Value[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// handleChunk stores one verified chunk segment.
func handleChunk(w http.ResponseWriter, r *http.Request, dir string) {
	// Cap comfortably above the client's chunk size (100 MiB) plus multipart
	// field overhead, so a chunk POST is never rejected before it even reaches
	// the checksum check below.
	r.Body = http.MaxBytesReader(w, r.Body, 160<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	name := strings.TrimSpace(multipartValue(r, "name"))
	uploadID := multipartValue(r, "uploadId")
	if name == "" || uploadID == "" {
		writeErr(w, http.StatusBadRequest, "name and uploadId are required")
		return
	}
	var idx int
	if _, err := fmt.Sscanf(multipartValue(r, "index"), "%d", &idx); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid index")
		return
	}
	dst, _, err := cleanUserPath(dir, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// uploadID must match the resolved destination for this dir+name. This is the
	// only integrity check on the id (the path itself is re-confined above).
	if uploadID != encodeUploadID(dir, name) {
		writeErr(w, http.StatusBadRequest, "uploadId does not match name")
		return
	}
	st, err := readChunkState(dst)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no in-progress upload; call init first")
		return
	}
	if idx < 0 || idx >= st.TotalChunks {
		writeErr(w, http.StatusBadRequest, "index out of range")
		return
	}
	// Idempotent: a chunk already received is reported as done without rewriting
	// it, so a client retry after a flaky network succeeds instead of 4xx'ing.
	if st.Received[idx] {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "index": idx, "crc32": "", "resumed": true})
		return
	}
	files := r.MultipartForm.File["chunk"]
	if len(files) != 1 {
		writeErr(w, http.StatusBadRequest, "expected one chunk part")
		return
	}
	src, err := files[0].Open()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sum, werr := writeChunkSegment(dst, idx, src, multipartValue(r, "hash"))
	_ = src.Close()
	if werr != nil {
		writeErr(w, http.StatusBadRequest, werr.Error())
		return
	}
	// Re-read the state under the per-destination lock rather than reusing the
	// `st` read above: another concurrent chunk request for this same file may
	// have persisted its own Received update in the meantime, and writing back
	// our stale copy would silently erase it (see chunkStateLocks doc comment).
	unlock := lockChunkState(dst)
	defer unlock()
	st, err = readChunkState(dst)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no in-progress upload; call init first")
		return
	}
	st.Received[idx] = true
	if err := writeChunkState(dst, st); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "index": idx, "crc32": sum})
}

// chunkCompleteReq is the JSON body of /chunk/complete.
type chunkCompleteReq struct {
	Name      string `json:"name"`
	UploadID  string `json:"uploadId"`
	FileCRC32 string `json:"fileCRC32"`
}

// handleChunkComplete assembles the segments into the final file and verifies it.
func handleChunkComplete(w http.ResponseWriter, r *http.Request, dir string) {
	var req chunkCompleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	uploadID := req.UploadID
	fileCRC32 := req.FileCRC32
	if name == "" || uploadID == "" {
		writeErr(w, http.StatusBadRequest, "name and uploadId are required")
		return
	}
	dst, _, err := cleanUserPath(dir, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if uploadID != encodeUploadID(dir, name) {
		writeErr(w, http.StatusBadRequest, "uploadId does not match name")
		return
	}
	st, err := readChunkState(dst)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no in-progress upload")
		return
	}
	// Missing chunks is a conflict (client jumped ahead): report the gaps so the
	// client can re-send just those before completing again.
	if missing := missingChunks(st); len(missing) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   fmt.Sprintf("missing %d chunk(s)", len(missing)),
			"missing": missing,
		})
		return
	}
	sum, aerr := assembleChunks(dst, st, fileCRC32)
	if aerr != nil {
		writeErr(w, http.StatusBadRequest, aerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "crc32": sum, "path": name})
}

// chunkAbortReq is the JSON body of /chunk/abort.
type chunkAbortReq struct {
	Name     string `json:"name"`
	UploadID string `json:"uploadId"`
}

// handleChunkAbort removes all segment + state artifacts for an in-progress
// upload. It never deletes an already-completed destination file.
func handleChunkAbort(w http.ResponseWriter, r *http.Request, dir string) {
	var req chunkAbortReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	uploadID := req.UploadID
	if name == "" || uploadID == "" {
		writeErr(w, http.StatusBadRequest, "name and uploadId are required")
		return
	}
	dst, _, err := cleanUserPath(dir, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if uploadID != encodeUploadID(dir, name) {
		writeErr(w, http.StatusBadRequest, "uploadId does not match name")
		return
	}
	if st, err := readChunkState(dst); err == nil {
		removeChunkArtifacts(dst, st)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
