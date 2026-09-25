package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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

// Hard bounds on the declared upload layout (docs/SECURITY-AUDIT.md M-1/M-2).
// The size/chunkSize/totalChunks triple comes from the client, and every
// server-side loop (missing chunks, segment cleanup, assembly) is O(totalChunks)
// — so init refuses layouts that are arithmetically impossible or absurdly
// large, per-chunk writes are pinned to their declared byte range, and the
// assembled file is re-checked against the declared size before it is accepted.
const (
	// maxUploadChunks bounds TotalChunks so missingChunks/removeChunkArtifacts
	// can never be turned into a multi-GB allocation / million-syscall loop by
	// a hostile client (100k chunks * 100 MiB ≈ 9.5 PB — far beyond any real
	// upload, so the cap can never reject an honest one).
	maxUploadChunks = 100_000
	// maxDeclaredChunkSize matches the per-chunk request body cap in
	// handleChunk (160 MiB): a bigger declared chunk could never be delivered
	// in one POST anyway, so agreeing on the ceiling here keeps the layout
	// honest end to end.
	maxDeclaredChunkSize = 160 << 20
)

// chunkUploadState is the on-disk resume record. Stored as JSON at <dst>.mudppart.
type chunkUploadState struct {
	Size        int64        `json:"size"`        // total file size in bytes
	ChunkSize   int64        `json:"chunkSize"`   // bytes per chunk (last may be smaller)
	TotalChunks int          `json:"totalChunks"` // number of chunks
	FileCRC32   string       `json:"fileCRC32"`   // expected whole-file crc32 ("" if unknown)
	Received    map[int]bool `json:"received"`    // index -> received
	// UploadID is the random opaque handle returned by init and echoed by
	// chunk/complete/abort. It deliberately encodes NOTHING about the server
	// filesystem — the earlier scheme used the resolved absolute path, which
	// disclosed the netdisk layout (and Docker volume mountpoints) to any
	// activated user (docs/SECURITY-AUDIT.md M-3).
	UploadID string `json:"uploadId"`
	// RelPath is the user-relative destination (e.g. "docs/big.bin"). It is the
	// source of truth for where segments and the final file live; the uploadId a
	// client carries is just an opaque handle matched against UploadID above.
	RelPath string `json:"relPath" ` //nolint:revive
}

// lockIdleTTL bounds how long a lockCache entry survives without an acquire.
// It must sit far above the longest critical section these locks guard (a
// 160 MiB segment write over a slow link): entries are stamped on every
// acquire, so a held or waited-on entry always looks fresh and only genuinely
// idle ones are evicted.
const lockIdleTTL = time.Hour

// lockCache is a keyed mutex pool with idle eviction. The keys are unbounded —
// one per destination and one per destination#index ever uploaded — so without
// eviction the tables would grow for the lifetime of the process.
type lockCache struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
	lastGC  time.Time
}

type lockEntry struct {
	mu   sync.Mutex
	seen time.Time
}

// lock acquires the mutex for key and returns the unlock func the caller must
// defer. The sweep runs on the acquire path at most once per TTL, mirroring
// RateLimiter.gcLocked. Eviction cannot strand a live entry: seen is stamped
// under c.mu before the entry mutex is taken, so an entry old enough to evict
// has neither a holder nor a waiter. The only escape is a goroutine frozen
// between stamp and Lock for a full TTL, and even then the failure degrades to
// one interleaved segment write — which the per-chunk and whole-file CRC32
// checks catch and the client re-sends.
func (c *lockCache) lock(key string) func() {
	now := time.Now()
	c.mu.Lock()
	if now.Sub(c.lastGC) >= lockIdleTTL {
		for k, e := range c.entries {
			if now.Sub(e.seen) > lockIdleTTL {
				delete(c.entries, k)
			}
		}
		c.lastGC = now
	}
	e, ok := c.entries[key]
	if !ok {
		e = &lockEntry{seen: now}
		c.entries[key] = e
	}
	e.seen = now
	c.mu.Unlock()
	e.mu.Lock()
	return e.mu.Unlock
}

// chunkStateLocks serializes the read-modify-write of one destination's resume
// state across concurrent chunk uploads for that SAME file (the client
// uploads chunks with concurrency > 1 by design). Without this, two goroutines
// can each read the same Received map, mark a different index, and the second
// write silently clobbers the first — the chunk's bytes are on disk, but the
// record that it arrived is lost, which later surfaces as a bogus "missing
// chunks" error on complete. Keyed by destination path; idle entries are
// evicted (see lockCache).
var chunkStateLocks = &lockCache{entries: map[string]*lockEntry{}}

// lockChunkState acquires the per-destination lock, returning the unlock func
// the caller must defer.
func lockChunkState(dst string) func() {
	return chunkStateLocks.lock(dst)
}

// chunkSegmentLocks serializes writeChunkSegment for one (destination, index)
// pair. A client retry (timeout, flaky connection) can leave the original
// request for a chunk still in flight when the retry arrives; without this,
// both would open the same segment file O_TRUNC and stream into it
// concurrently, interleaving bytes into a corrupt segment. Keyed by
// "dst#index" rather than just dst, so chunks at different indices — which the
// client intentionally uploads with concurrency > 1 — are not serialized
// against each other. Idle entries are evicted (see lockCache).
var chunkSegmentLocks = &lockCache{entries: map[string]*lockEntry{}}

// lockChunkSegment acquires the per-(destination,index) lock, returning the
// unlock func the caller must defer.
func lockChunkSegment(dst string, index int) func() {
	return chunkSegmentLocks.lock(fmt.Sprintf("%s#%d", dst, index))
}

// chunkStatePath returns the path of the resume state file for a destination.
func chunkStatePath(dst string) string { return dst + mudppartSuffix }

// chunkSegmentPath returns the path of the segment file for chunk `index`.
func chunkSegmentPath(dst string, index int) string {
	return fmt.Sprintf("%s.%d", chunkStatePath(dst), index)
}

// newUploadID mints the random opaque handle for one upload session.
func newUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// readChunkState loads the resume record, returning (nil, os.IsNotExist) when
// there is no in-progress upload for dst. States whose layout violates the
// hard bounds (e.g. written by a pre-hardening server under a hostile client)
// are rejected outright: every O(TotalChunks) loop downstream must stay
// genuinely bounded even for state files that predate the init validation.
func readChunkState(dst string) (*chunkUploadState, error) {
	data, err := readStateFileWithRetry(chunkStatePath(dst))
	if err != nil {
		return nil, err
	}
	var st chunkUploadState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if st.TotalChunks <= 0 || st.TotalChunks > maxUploadChunks || st.ChunkSize <= 0 || st.ChunkSize > maxDeclaredChunkSize || st.Size < 0 {
		return nil, fmt.Errorf("invalid resume state for %s", filepath.Base(dst))
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
	return renameWithRetry(tmp, statePath)
}

// readStateFileWithRetry wraps os.ReadFile with a short retry loop for
// transient Windows errors — the reader-side twin of renameWithRetry. The
// state file is replaced via write-temp + rename, and a reader that opens the
// destination while that rename commits can be refused with a sharing
// violation / ACCESS_DENIED even though nothing is wrong; handleChunk would
// turn that into a bogus 404 ("no in-progress upload") and lose the chunk. A
// missing file is returned immediately: it is the normal "no upload in
// progress" answer, not a transient failure.
func readStateFileWithRetry(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	for attempt := 0; err != nil && !errors.Is(err, os.ErrNotExist) && attempt < 5; attempt++ {
		time.Sleep(time.Duration(attempt+1) * 2 * time.Millisecond)
		data, err = os.ReadFile(path)
	}
	return data, err
}

// renameWithRetry wraps os.Rename with a short retry loop. On Windows,
// replacing a file that is momentarily open elsewhere — a concurrent
// readChunkState, the search indexer or AV — fails with ACCESS_DENIED even
// though nothing is wrong; readers here hold the file for microseconds, so a
// few backed-off retries turns an intermittent 400 ("rename ... Access is
// denied", see docs/SECURITY-AUDIT.md reliability note) into a plain success.
func renameWithRetry(oldname, newname string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.Rename(oldname, newname); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
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
// so a retransmit cleanly replaces a prior bad segment. expectedLen is the
// chunk's declared byte count (chunkByteRange): a body that is short (truncated
// upload) or long (quota-bypass attempt, docs/SECURITY-AUDIT.md M-1) is
// rejected and the segment removed. Returns the computed CRC32.
func writeChunkSegment(dst string, index int, src io.Reader, expectedCRC32 string, expectedLen int64) (string, error) {
	if expectedLen < 0 {
		return "", fmt.Errorf("chunk %d: invalid declared length", index)
	}
	segPath := chunkSegmentPath(dst, index)
	if err := os.MkdirAll(filepath.Dir(segPath), 0o750); err != nil {
		return "", err
	}
	f, err := os.OpenFile(segPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}
	hash := crc32.NewIEEE()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), src)
	_ = f.Close()
	if copyErr != nil {
		_ = os.Remove(segPath)
		return "", copyErr
	}
	// Length pin: the bytes on disk must be exactly what the declared layout
	// promised for this index. Anything else — a short body from a flaky link
	// or an oversized body smuggling past the quota projection — is discarded.
	if n != expectedLen {
		_ = os.Remove(segPath)
		return "", fmt.Errorf("chunk %d: got %d bytes, want %d", index, n, expectedLen)
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
	var written int64
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
		fi, serr := seg.Stat()
		_ = seg.Close()
		if serr != nil {
			_ = out.Close()
			_ = os.Remove(dst)
			return "", fmt.Errorf("stat segment %d: %w", i, serr)
		}
		written += fi.Size()
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	// Final size pin (docs/SECURITY-AUDIT.md M-1): the assembled file must be
	// exactly the declared Size. This is the definitive quota-bypass closure —
	// even if some path ever slipped an oversized segment through, the finished
	// file is still rejected and removed here.
	if written != st.Size {
		_ = os.Remove(dst)
		return "", fmt.Errorf("%w: assembled %d bytes, declared %d", ErrChecksumMismatch, written, st.Size)
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
	// Layout hardening (docs/SECURITY-AUDIT.md M-1/M-2): the declared triple
	// must be arithmetically consistent — totalChunks == ceil(size/chunkSize) —
	// and each component within its cap. Without this, a client could declare
	// a tiny size to pass the quota projection, then deliver arbitrarily many
	// oversized chunks; or declare an enormous totalChunks and make every
	// complete/abort allocate an O(n) missing-list. (size == 0 is rejected
	// implicitly: ceil(0/x) == 0 != totalChunks >= 1.)
	if req.ChunkSize > maxDeclaredChunkSize {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("chunkSize exceeds %d bytes", maxDeclaredChunkSize))
		return
	}
	if req.TotalChunks > maxUploadChunks {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("totalChunks exceeds %d", maxUploadChunks))
		return
	}
	if want := (req.Size + req.ChunkSize - 1) / req.ChunkSize; int64(req.TotalChunks) != want {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("totalChunks %d does not match size/chunkSize (want %d)", req.TotalChunks, want))
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
	// (Re)mint the opaque handle. Fresh states always get one; resumed states
	// keep theirs so an interrupted client's handle stays valid, except for
	// states written before the handle existed (pre-upgrade in-flight uploads),
	// which are upgraded in place on the resume-init that is already required
	// after any server restart.
	if st.UploadID == "" {
		id, ierr := newUploadID()
		if ierr != nil {
			writeErr(w, http.StatusInternalServerError, ierr.Error())
			return
		}
		st.UploadID = id
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
		"uploadId":    st.UploadID,
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
	st, err := readChunkState(dst)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no in-progress upload; call init first")
		return
	}
	// uploadID is the random handle init minted for this exact destination. The
	// path itself is re-confined above, so this is an integrity check, not the
	// confinement (docs/SECURITY-AUDIT.md M-3: the handle carries no path).
	if uploadID != st.UploadID {
		writeErr(w, http.StatusBadRequest, "uploadId does not match name")
		return
	}
	if idx < 0 || idx >= st.TotalChunks {
		writeErr(w, http.StatusBadRequest, "index out of range")
		return
	}
	// Held through the segment write below so a retry for this exact chunk
	// index (timeout, flaky connection) can't run writeChunkSegment
	// concurrently with the original request and interleave bytes into a
	// corrupt segment; see chunkSegmentLocks doc comment.
	unlockSeg := lockChunkSegment(dst, idx)
	defer unlockSeg()
	// Idempotent: a chunk already received is reported as done without rewriting
	// it, so a client retry after a flaky network succeeds instead of 4xx'ing.
	// Re-read fresh now that the segment lock is held -- a request that was in
	// flight for this same index may have just finished and persisted it, and
	// the `st` read above (before the lock) could be stale.
	if fresh, ferr := readChunkState(dst); ferr == nil && fresh.Received[idx] {
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
	// Pin the segment to its declared byte range (chunkByteRange): a short or
	// oversized body is rejected by writeChunkSegment itself.
	segStart, segEnd := chunkByteRange(st, idx)
	sum, werr := writeChunkSegment(dst, idx, src, multipartValue(r, "hash"), segEnd-segStart)
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

// handleChunkComplete assembles the segments into the final file and verifies
// it. onDone (may be nil) is called with the resolved destination path once
// assembly succeeds, so callers can drop their session tracking for the admin
// task list; it is NOT called on a missing-chunks conflict, since the upload
// is still in progress at that point.
func handleChunkComplete(w http.ResponseWriter, r *http.Request, dir string, onDone func(dst string)) {
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
	st, err := readChunkState(dst)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no in-progress upload")
		return
	}
	if uploadID != st.UploadID {
		writeErr(w, http.StatusBadRequest, "uploadId does not match name")
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
	if onDone != nil {
		onDone(dst)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "crc32": sum, "path": name})
}

// chunkAbortReq is the JSON body of /chunk/abort.
type chunkAbortReq struct {
	Name     string `json:"name"`
	UploadID string `json:"uploadId"`
}

// handleChunkAbort removes all segment + state artifacts for an in-progress
// upload. It never deletes an already-completed destination file. onDone (may
// be nil) is called with the resolved destination path so callers can drop
// their session tracking for the admin task list.
func handleChunkAbort(w http.ResponseWriter, r *http.Request, dir string, onDone func(dst string)) {
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
	if st, err := readChunkState(dst); err == nil {
		// Only the handle init minted for this destination may abort it.
		if uploadID != st.UploadID {
			writeErr(w, http.StatusBadRequest, "uploadId does not match name")
			return
		}
		removeChunkArtifacts(dst, st)
	}
	if onDone != nil {
		onDone(dst)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
