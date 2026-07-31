package server

import (
	"archive/zip"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"mudp/internal/store"
	"mudp/web"
)

type fileItem struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

func (a *App) userNetdiskRoot(u *store.User) (string, error) {
	root, err := a.db.NetdiskPathForUser(u.ID)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("netdisk path is not configured for your group")
	}
	return a.ensureUserNetdisk(u, root)
}

// userBackupRoot resolves the per-user directory on the backup disk. Unlike
// userNetdiskRoot it does NOT auto-create the directory: backup disks are often
// slow mechanical drives and we only want to touch them when a backup actually
// runs. Callers that need the dir to exist should os.MkdirAll it themselves.
func (a *App) userBackupRoot(u *store.User) (string, error) {
	root, err := a.db.BackupPathForUser(u.ID)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("backup path is not configured for your group")
	}
	return filepath.Join(root, fmt.Sprintf("%s-%d", sanitizePathPart(u.Username), u.ID)), nil
}

func (a *App) ensureUserNetdisk(u *store.User, root string) (string, error) {
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", sanitizePathPart(u.Username), u.ID))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	// Containers bind-mount this dir as /workspace and typically write into
	// it as root; every path that touches the netdisk (file ops here as well
	// as container creation in server.go) runs through this function, so
	// fixing it here keeps the mudp service's own write access self-healing
	// even for folders a container created root-owned before or between
	// panel requests, not just at container-creation time.
	fixNetdiskACL(dir)
	return dir, nil
}

func sanitizePathPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "user"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}

// cleanUserPath resolves a user-supplied relative path against root and refuses
// anything that escapes it. Two checks are needed, not one:
//
//   - lexically, so "../.." cannot climb out; and
//   - after symlink resolution, because a lexical check alone only inspects the
//     final component. Every user's netdisk is bind-mounted into their own
//     containers (as /workspace), where they are root and can create links
//     freely: `ln -s / pwn` then "pwn/etc/shadow" passes a lexical check and an
//     isSymlink() test on the leaf, while the open still follows the link out to
//     the host filesystem. The same applies to managed volumes.
//
// It returns the symlink-resolved absolute path, so callers act on the location
// that was actually validated.
func cleanUserPath(root, rel string) (string, string, error) {
	rel = strings.TrimPrefix(filepath.Clean("/"+rel), string(filepath.Separator))
	full := filepath.Join(root, rel)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if !pathWithin(rootAbs, fullAbs) {
		return "", "", fmt.Errorf("invalid path")
	}
	// The root itself may legitimately sit behind a symlink (e.g. a netdisk path
	// pointing at a mounted disk), so resolve both sides before comparing.
	resolvedRoot, err := resolveExistingPath(rootAbs)
	if err != nil {
		return "", "", err
	}
	resolvedFull, err := resolveExistingPath(fullAbs)
	if err != nil {
		return "", "", err
	}
	if !pathWithin(resolvedRoot, resolvedFull) {
		return "", "", fmt.Errorf("invalid path")
	}
	return resolvedFull, rel, nil
}

// pathWithin reports whether p is root itself or lives underneath it.
func pathWithin(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// resolveExistingPath returns p with every symlink component resolved. Paths
// that do not exist yet are normal here — mkdir, rename and upload targets are
// all created after validation — so when EvalSymlinks fails the deepest
// existing ancestor is resolved and the remaining components are appended
// verbatim. Those trailing components cannot themselves be links: they do not
// exist.
//
// A resolved-then-open sequence is still theoretically racy (a component could
// be swapped for a symlink in between), but the window requires the attacker to
// win a race inside their own directory, and closing it fully needs
// per-component openat(O_NOFOLLOW), which has no portable Windows equivalent.
func resolveExistingPath(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		// Reached the filesystem root without finding anything that exists.
		return p, nil
	}
	resolvedParent, err := resolveExistingPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(p)), nil
}

func (a *App) netdiskList(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, rel, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Use Lstat so symlinks are not followed; skip them entirely.
		li, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if rel == "." {
			p = entry.Name()
		}
		items = append(items, fileItem{Name: entry.Name(), Path: p, Dir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(rel), "items": items})
}

func (a *App) netdiskMkdir(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	full, _, err := cleanUserPath(root, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.MkdirAll(full, 0750))
}

func (a *App) netdiskDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	for _, p := range req.Paths {
		full, _, err := cleanUserPath(root, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.RemoveAll(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) netdiskRename(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil || req.From == "" || req.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	from, _, err := cleanUserPath(root, req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, _, err := cleanUserPath(root, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.Rename(from, to))
}

func (a *App) netdiskCopy(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Items []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"items"`
		Move   bool   `json:"move"`
		Policy string `json:"policy"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "items are required")
		return
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if policy == "" {
		policy = "rename"
	}
	if policy != "overwrite" && policy != "skip" && policy != "rename" {
		writeErr(w, http.StatusBadRequest, "policy must be overwrite, skip or rename")
		return
	}

	var results []map[string]string
	count := 0
	for _, item := range req.Items {
		res := map[string]string{"from": item.From, "to": item.To}
		from, _, err := cleanUserPath(root, item.From)
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			continue
		}
		to, _, err := cleanUserPath(root, item.To)
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			continue
		}
		if err := netdiskCopyOne(from, to, req.Move, policy); err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
		} else {
			if req.Move {
				res["status"] = "moved"
			} else {
				res["status"] = "copied"
			}
			count++
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "results": results})
}

func netdiskCopyOne(from, to string, move bool, policy string) error {
	fromInfo, err := os.Stat(from)
	if err != nil {
		return err
	}
	// If the destination is an existing directory, place the source inside it
	// preserving its base name. This matches how the UI sends the current folder
	// path as the copy/move destination.
	if toInfo, err := os.Stat(to); err == nil && toInfo.IsDir() {
		to = filepath.Join(to, filepath.Base(from))
	}
	if _, err := os.Stat(to); err == nil {
		switch policy {
		case "skip":
			return nil
		case "rename":
			to = nextFreeName(filepath.Dir(to), filepath.Base(to))
		case "overwrite":
			if fromInfo.IsDir() {
				return fmt.Errorf("cannot overwrite a directory")
			}
			if err := os.Remove(to); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(to), 0750); err != nil {
		return err
	}
	if move {
		return os.Rename(from, to)
	}
	return copyPathWithPolicy(from, to, policy)
}

// uploadDestPath resolves where the i-th uploaded file part lands under dir.
// relPaths holds the client-sent relative paths ("sub/dir/file.txt") for a
// folder upload, one per part in part order; a missing or empty entry falls
// back to the part's own filename, which Go has already stripped to a base
// name. The result is confined to dir by cleanUserPath, so a hostile "paths"
// value cannot escape the netdisk root, and must name a file inside it — a
// value that resolves to dir itself (empty, ".", "sub/") is rejected rather
// than turned into a write over the directory.
func uploadDestPath(dir string, relPaths []string, i int, fh *multipart.FileHeader) (string, error) {
	name := fh.Filename
	if i < len(relPaths) {
		if p := strings.TrimSpace(relPaths[i]); p != "" {
			name = p
		}
	}
	dstPath, _, err := cleanUserPath(dir, name)
	if err != nil {
		return "", err
	}
	// Compare against the canonical form of dir (not the raw string) so a value
	// that normalizes back to the directory — "", ".", "/", "../.." — is caught.
	dirPath, _, err := cleanUserPath(dir, "")
	if err != nil {
		return "", err
	}
	if dstPath == dirPath {
		return "", fmt.Errorf("invalid file name")
	}
	return dstPath, nil
}

func (a *App) netdiskUpload(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Cap the whole request body so a single upload cannot exhaust server
	// memory/disk (the 2 GiB ParseMultipartForm argument only bounds the RAM
	// buffer, not total bytes). 2 GiB matches the previous effective ceiling.
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	dir, _, err := cleanUserPath(root, r.FormValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "no files uploaded")
		return
	}
	// Folder uploads carry their in-folder path in a parallel "paths" field,
	// one value per file part in the same order. It cannot ride on the part's
	// filename: RFC 7578 §4.2 forbids directory information there and Go's
	// multipart parser enforces it by reducing Filename to its base name, which
	// would flatten "docs/a/b.txt" into "b.txt".
	relPaths := r.MultipartForm.Value["paths"]
	// Per-file CRC32 digests the client computed (parallel to "paths"/"files").
	// Empty/absent for legacy clients: files are still written + checksummed.
	hashes := r.MultipartForm.Value["hashes"]
	var results []uploadResult

	// Pre-compute how much additional space is required, accounting for
	// partially-uploaded files that may be resumed.
	used := dirSize(root)
	var additional int64
	projected := used
	for i, fh := range files {
		dstPath, err := uploadDestPath(dir, relPaths, i, fh)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var existing int64
		if info, err := os.Stat(dstPath); err == nil {
			existing = info.Size()
		}
		add := fh.Size - existing
		if add < 0 {
			add = 0
		}
		additional += add
		projected += add
	}
	if u.NetdiskQuotaBytes > 0 && projected > u.NetdiskQuotaBytes {
		writeErr(w, http.StatusInsufficientStorage, "upload would exceed netdisk quota")
		return
	}
	if additional > 0 {
		free, err := diskFree(dir)
		if err == nil && free >= 0 && additional > free {
			writeErr(w, http.StatusInsufficientStorage, "not enough free disk space")
			return
		}
	}

	for i, fh := range files {
		src, err := fh.Open()
		if err != nil {
			// One unreadable part no longer fails the whole batch: record the
			// failure for this file and continue, so the other files still land.
			results = append(results, uploadResult{Path: netdiskResultPath(relPaths, i, fh), Error: err.Error()})
			continue
		}
		dstPath, err := uploadDestPath(dir, relPaths, i, fh)
		if err != nil {
			_ = src.Close()
			results = append(results, uploadResult{Path: netdiskResultPath(relPaths, i, fh), Error: err.Error()})
			continue
		}
		// Client-supplied per-file CRC32 (parallel to "paths", one per file). Empty
		// for legacy clients / browsers without a hashing Worker: the file is
		// still written and checksummed, just not compared.
		expected := ""
		if i < len(hashes) {
			expected = hashes[i]
		}
		sum, werr := writeFileWithCRC32(dstPath, src, fh, expected)
		_ = src.Close()
		if werr != nil {
			results = append(results, uploadResult{Path: netdiskResultPath(relPaths, i, fh), CRC32: sum, Error: werr.Error()})
		} else {
			results = append(results, uploadResult{Path: netdiskResultPath(relPaths, i, fh), CRC32: sum})
		}
	}
	// ok reflects only fully-saved files so the client can tell a partial failure
	// (some files landed, some didn't) from total success.
	okCount := len(files) - countFailedResults(results)
	writeJSON(w, http.StatusOK, map[string]any{"ok": okCount == len(files), "count": len(files), "results": results})
}

// netdiskResultPath picks the user-facing identifier for a result row: the
// in-folder relative path when present, otherwise the part's filename.
func netdiskResultPath(relPaths []string, i int, fh *multipart.FileHeader) string {
	if i < len(relPaths) && relPaths[i] != "" {
		return relPaths[i]
	}
	return fh.Filename
}

// countFailedResults tallies how many per-file results carry an error.
func countFailedResults(results []uploadResult) int {
	n := 0
	for _, r := range results {
		if r.Error != "" {
			n++
		}
	}
	return n
}

// ---- Large-file chunked/resumable upload (netdisk) --------------------------
// Files >= the client's threshold are uploaded in CRC32-verified chunks so a
// multi-GB transfer can resume after a drop/refresh instead of restarting. The
// shared protocol logic lives in chunkupload.go; these handlers only resolve the
// netdisk root + path and (for init) enforce quota.

func (a *App) netdiskChunkInit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req chunkInitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// req.Path is the folder the user is currently browsing (e.g. "docs/2024");
	// resolve it against the root so the file lands there instead of always at
	// the netdisk root.
	dir, _, err := cleanUserPath(root, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Quota/disk projection: reject if the additional bytes would blow the
	// user's netdisk quota or leave no free disk space.
	quotaCheck := func(add int64) error {
		if u.NetdiskQuotaBytes > 0 {
			used := dirSize(root)
			if used+add > u.NetdiskQuotaBytes {
				return fmt.Errorf("upload would exceed netdisk quota")
			}
		}
		if add > 0 {
			if free, e := diskFree(root); e == nil && free >= 0 && add > free {
				return fmt.Errorf("not enough free disk space")
			}
		}
		return nil
	}
	handleChunkInit(w, dir, req.Name, req, quotaCheck)
}

// netdiskChunkDir resolves the folder the client is uploading into (the
// "path" query parameter, e.g. "docs/2024") against the caller's netdisk root.
// The /chunk, /chunk/complete and /chunk/abort requests all carry it as a URL
// query parameter (rather than in the JSON/multipart body) so it can be
// resolved uniformly before touching the endpoint-specific payload.
func (a *App) netdiskChunkDir(r *http.Request) (string, error) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		return "", err
	}
	dir, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	return dir, err
}

func (a *App) netdiskChunk(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	dir, err := a.netdiskChunkDir(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunk(w, r, dir)
}

func (a *App) netdiskChunkComplete(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	dir, err := a.netdiskChunkDir(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunkComplete(w, r, dir)
}

func (a *App) netdiskChunkAbort(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	dir, err := a.netdiskChunkDir(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunkAbort(w, r, dir)
}

func (a *App) netdiskDownload(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if !info.IsDir() {
		serveFileDownload(w, r, full, info.Name())
		return
	}
	serveZipDownload(w, full, info.Name()+".zip")
}

// netdiskRaw serves a single file inline so the browser can preview it
// (PDF / image / video / audio / text). It shares the same path and symlink
// guards as download, only the Content-Disposition differs.
func (a *App) netdiskRaw(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is a folder")
		return
	}
	serveFileInline(w, r, full, info.Name())
}

func (a *App) netdiskQuota(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	used, stale, updatedAt := a.dirSizeCached(root)
	q := map[string]any{"usedBytes": used}
	if stale {
		q["usedEstimating"] = true
	}
	if updatedAt != "" {
		q["usedUpdatedAt"] = updatedAt
	}
	if u.NetdiskQuotaBytes > 0 {
		q["totalBytes"] = u.NetdiskQuotaBytes
		free := u.NetdiskQuotaBytes - used
		if free < 0 {
			free = 0
		}
		q["freeBytes"] = free
	}
	if free, err := diskFree(root); err == nil && free >= 0 {
		q["diskFreeBytes"] = free
	}
	writeJSON(w, http.StatusOK, q)
}

// netdiskUsageAdmin reports each user's current netdisk usage so admins can see
// how much space everyone is consuming against their quota. Users with no
// configured netdisk path report usedBytes=0 and configured=false.
func (a *App) netdiskUsageAdmin(w http.ResponseWriter, r *http.Request) {
	users, err := a.db.Users()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type userUsage struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		UsedBytes   int64  `json:"usedBytes"`
		QuotaBytes  int64  `json:"quotaBytes"`
		Configured  bool   `json:"configured"`
		PathMissing bool   `json:"pathMissing"`
	}
	out := make([]userUsage, 0, len(users))
	for _, u := range users {
		entry := userUsage{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			QuotaBytes:  u.NetdiskQuotaBytes,
		}
		// Resolve this user's netdisk root the same way their own requests do.
		// A missing on-disk directory or an unconfigured group path is reported
		// rather than counted as an error so the table still renders.
		root, err := a.userNetdiskRoot(&u)
		if err != nil {
			entry.Configured = false
		} else {
			entry.Configured = true
			if _, err := os.Stat(root); err != nil {
				entry.PathMissing = true
			} else {
				used, _, _ := a.dirSizeCached(root)
				entry.UsedBytes = used
			}
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// dirSizeCached returns the most recent cached size for a root and triggers an
// async refresh when missing/stale. stale=true means the value is currently an
// estimate from cache (or zero while first scan is pending).
func (a *App) dirSizeCached(root string) (bytes int64, stale bool, updatedAt string) {
	const ttl = 2 * time.Minute
	now := time.Now()
	a.dirSizeMu.Lock()
	entry, ok := a.dirSizeCache[root]
	running := a.dirSizeRunning[root]
	needRefresh := !ok || now.Sub(entry.updated) > ttl
	if needRefresh && !running {
		a.dirSizeRunning[root] = true
		go a.refreshDirSize(root)
		running = true
	}
	a.dirSizeMu.Unlock()
	if !ok {
		return 0, true, ""
	}
	return entry.bytes, needRefresh || running, entry.updated.Format(time.RFC3339)
}

func (a *App) refreshDirSize(root string) {
	// Limit heavy recursive scans to one worker so requests remain responsive
	// even when many users/directories are queued for refresh.
	a.dirSizeSemaphore <- struct{}{}
	bytes := dirSize(root)
	<-a.dirSizeSemaphore
	a.dirSizeMu.Lock()
	a.dirSizeCache[root] = dirSizeEntry{bytes: bytes, updated: time.Now()}
	delete(a.dirSizeRunning, root)
	a.dirSizeMu.Unlock()
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (a *App) netdiskShareCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Paths       []string `json:"paths"`
		Name        string   `json:"name"`
		Permanent   bool     `json:"permanent"`
		ExpiresAt   string   `json:"expiresAt"`
		ExpiresDays int      `json:"expiresDays"`
		Password    string   `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	var clean []string
	for _, p := range req.Paths {
		full, rel, err := cleanUserPath(root, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := os.Stat(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		clean = append(clean, filepath.ToSlash(rel))
	}
	token, err := a.uniqueShareToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(clean[0])
	}
	expiresAt := ""
	if !req.Permanent {
		if req.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, req.ExpiresAt); err == nil && t.After(time.Now()) {
				expiresAt = t.Format(time.RFC3339)
			}
		}
		if expiresAt == "" {
			days := req.ExpiresDays
			if days <= 0 {
				days = 7
			}
			expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		}
	}
	passwordHash := ""
	password := strings.TrimSpace(req.Password)
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		passwordHash = string(hash)
	}
	if err := a.db.CreateNetdiskShare(u.ID, token, name, clean, expiresAt, req.Permanent, passwordHash, password); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "url": "/pan/" + token})
}

func (a *App) netdiskShares(w http.ResponseWriter, r *http.Request) {
	items, err := a.db.NetdiskShares(currentUser(r).ID)
	respond(w, items, err)
}

func (a *App) netdiskShareDelete(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	var req struct {
		Token  string   `json:"token"`
		Tokens []string `json:"tokens"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Token != "" {
		req.Tokens = append(req.Tokens, req.Token)
	}
	if len(req.Tokens) == 0 {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	respond(w, map[string]bool{"ok": true}, a.db.DeleteNetdiskShares(req.Tokens, currentUser(r).ID, false))
}

func (a *App) netdiskSharePublic(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if err := checkSharePassword(r, share); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "needsPassword": true})
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		// Share root: list each shared path as a flat top-level row. A shared
		// file is shown directly (no parent folder/breadcrumb); only a shared
		// folder appears as a navigable folder. This keeps "share a file inside
		// a folder" from exposing the folder path or its other contents.
		writeJSON(w, http.StatusOK, map[string]any{"share": share, "items": flatShareRootItems(ownerRoot, share.Paths), "readOnly": true, "path": ""})
		return
	}
	// Browsing into a folder: it must itself be shared or lead to a shared
	// item (e.g. the parent of a shared file). Direct files are not browseable.
	if !shareVisible(share.Paths, reqPath) {
		writeErr(w, http.StatusForbidden, "path is not in this share")
		return
	}
	full, rel, err := cleanUserPath(ownerRoot, reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is not a folder")
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		li, err := os.Lstat(filepath.Join(full, entry.Name()))
		if err != nil || li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if rel == "." || rel == "" {
			p = entry.Name()
		}
		// Inside a browsed folder, show only entries that are shared themselves
		// or that lead to a shared item, so unshared siblings stay hidden.
		if !shareVisible(share.Paths, p) {
			continue
		}
		items = append(items, fileItem{Name: entry.Name(), Path: p, Dir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": share, "items": items, "readOnly": true, "path": filepath.ToSlash(rel)})
}

func (a *App) netdiskSharePage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if share, _, err := a.resolveShare(token); err != nil {
		expired := err == errShareExpired
		renderShareUnavailable(w, expired)
		return
	} else if share.Token == "" {
		renderShareUnavailable(w, false)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Try to serve the rich standalone share page. Fall back to the inline page
	// when the file is unavailable so builds without the frontend asset still work.
	var raw []byte
	var err error
	if a.cfg.WebDir != "" {
		raw, err = os.ReadFile(filepath.Join(a.cfg.WebDir, "share.html"))
	} else {
		raw, err = fs.ReadFile(web.Files, "share.html")
	}
	if err == nil && len(raw) > 0 {
		// Inject the token into the meta tag share.js reads. It used to also be
		// written into an inline <script>, which would have forced 'unsafe-inline'
		// into the page CSP for no benefit.
		html := strings.Replace(string(raw), `<meta name="share-token" content=""`, fmt.Sprintf(`<meta name="share-token" content="%s"`, escapeHTMLAttr(token)), 1)
		_, _ = w.Write([]byte(html))
		return
	}
	renderFallbackSharePage(w, token)
}

func (a *App) netdiskShareDownload(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if err := checkSharePassword(r, share); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "needsPassword": true})
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" && len(share.Paths) == 1 {
		reqPath = share.Paths[0]
	}
	if !shareContains(share.Paths, reqPath) {
		writeErr(w, http.StatusForbidden, "path is not in this share")
		return
	}
	full, _, err := cleanUserPath(ownerRoot, reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		serveZipDownload(w, full, info.Name()+".zip")
		return
	}
	serveFileDownload(w, r, full, info.Name())
}

// netdiskShareRaw serves a single file inline on the public share page so a
// visitor can preview it. Same resolution + password + scope checks as the
// download handler; only the Content-Disposition differs.
func (a *App) netdiskShareRaw(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if err := checkSharePassword(r, share); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "needsPassword": true})
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" && len(share.Paths) == 1 {
		reqPath = share.Paths[0]
	}
	if !shareContains(share.Paths, reqPath) {
		writeErr(w, http.StatusForbidden, "path is not in this share")
		return
	}
	full, _, err := cleanUserPath(ownerRoot, reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is a folder")
		return
	}
	serveFileInline(w, r, full, info.Name())
}

// planShareSave builds the copy tasks for saving shared paths into the user's
// destination directory. Each source is placed directly under dstDir using its
// path relative to the owner's netdisk root, preserving the original hierarchy
// without adding an extra share-name folder.
func planShareSave(dstDir, ownerRoot string, share store.NetdiskShare, reqPaths []string, policy string) ([]copyTask, int64) {
	var toCopy []copyTask
	var required int64
	plannedTargets := make(map[string]bool)
	for _, p := range reqPaths {
		if !shareContains(share.Paths, p) {
			continue
		}
		src, rel, err := cleanUserPath(ownerRoot, p)
		if err != nil {
			continue
		}
		size := pathSize(src)
		// Preserve the original hierarchy by saving under <dst>/<rel>.
		targetBase := filepath.Join(dstDir, rel)
		if policy == "rename" {
			targetBase = nextFreeNameWithPlanned(filepath.Dir(targetBase), filepath.Base(targetBase), plannedTargets)
		}
		plannedTargets[targetBase] = true
		toCopy = append(toCopy, copyTask{src: src, rel: rel, target: targetBase, name: filepath.Base(rel), size: size})
		required += size
	}
	return toCopy, required
}

func (a *App) netdiskShareSave(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify netdisk")
		return
	}
	dstRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Token  string   `json:"token"`
		Paths  []string `json:"paths"`
		To     string   `json:"to"`
		Policy string   `json:"policy"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	share, ownerRoot, err := a.resolveShare(req.Token)
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if len(req.Paths) == 0 {
		req.Paths = share.Paths
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if policy == "" {
		policy = "rename"
	}
	if policy != "overwrite" && policy != "skip" && policy != "rename" {
		writeErr(w, http.StatusBadRequest, "policy must be overwrite, skip or rename")
		return
	}
	dstDir, _, err := cleanUserPath(dstRoot, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate selection and compute required space.
	toCopy, required := planShareSave(dstDir, ownerRoot, share, req.Paths, policy)

	// Quota / free-space guard.
	if required > 0 {
		free, err := diskFree(dstDir)
		if err == nil && free >= 0 && required > free {
			writeErr(w, http.StatusInsufficientStorage, "not enough free space on destination disk")
			return
		}
	}

	count := 0
	results := make([]map[string]string, 0, len(toCopy))
	for _, t := range toCopy {
		relTarget, _ := filepath.Rel(dstDir, t.target)
		res := map[string]string{"path": filepath.ToSlash(t.rel), "target": filepath.ToSlash(relTarget)}
		dstPath := t.target
		if policy == "skip" {
			if _, err := os.Stat(dstPath); err == nil {
				res["status"] = "skipped"
				results = append(results, res)
				continue
			}
		}
		if err := copyPathWithPolicy(t.src, dstPath, policy); err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
		} else {
			res["status"] = "saved"
			count++
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "results": results})
}

func (a *App) resolveShare(token string) (store.NetdiskShare, string, error) {
	share, err := a.db.NetdiskShare(strings.TrimSpace(token))
	if err != nil {
		return share, "", err
	}
	if share.Expired {
		return share, "", errShareExpired
	}
	owner, err := a.db.UserByID(share.OwnerID)
	if err != nil {
		return share, "", err
	}
	root, err := a.userNetdiskRoot(owner)
	return share, root, err
}

var errShareExpired = fmt.Errorf("external link expired")

// checkSharePassword verifies the password for password-protected shares.
// It returns nil when no password is required or the supplied password matches.
func checkSharePassword(r *http.Request, share store.NetdiskShare) error {
	if !share.HasPassword {
		return nil
	}
	password := r.URL.Query().Get("password")
	if password == "" {
		password = r.Header.Get("X-Share-Password")
	}
	if password == "" {
		return fmt.Errorf("password required")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(share.PasswordHash), []byte(password)); err != nil {
		return fmt.Errorf("incorrect password")
	}
	return nil
}

func (a *App) netdiskSharesAdmin(w http.ResponseWriter, r *http.Request) {
	items, err := a.db.AllNetdiskShares()
	respond(w, items, err)
}

func (a *App) netdiskSharesDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tokens []string `json:"tokens"`
		Token  string   `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Token != "" {
		req.Tokens = append(req.Tokens, req.Token)
	}
	if len(req.Tokens) == 0 {
		writeErr(w, http.StatusBadRequest, "tokens are required")
		return
	}
	respond(w, map[string]bool{"ok": true}, a.db.DeleteNetdiskShares(req.Tokens, 0, true))
}

// isExecutableContentType reports whether a Content-Type would let the response
// body run as code in the panel's origin if a browser rendered it directly.
// Such a body is always sent as a download, never inline.
func isExecutableContentType(ct string) bool {
	base := strings.TrimSpace(strings.ToLower(ct))
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch base {
	case "text/html", "application/xhtml+xml", "application/xml", "text/xml",
		"application/javascript", "text/javascript", "application/x-javascript":
		return true
	}
	return false
}

// needsSandbox reports whether an inline body must be isolated into an opaque
// origin. SVG is the case that matters: it displays fine sandboxed but can
// otherwise carry script.
func needsSandbox(ct string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "image/svg")
}

func serveFileDownload(w http.ResponseWriter, r *http.Request, full, name string) {
	// Refuse symlinks so a user cannot download arbitrary host files.
	if isSymlink(full) {
		writeErr(w, http.StatusForbidden, "symlinks are not allowed")
		return
	}
	f, err := openNoFollow(full)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusForbidden, "not a regular file")
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	// An attachment is not rendered, but nosniff plus a neutral type removes any
	// remaining chance of the browser deciding otherwise.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// serveFileInline streams a single regular file with Content-Disposition:
// inline so the browser can render it (PDF/image/video/audio/text preview). It
// shares the symlink + regular-file guards of serveFileDownload and relies on
// http.ServeContent for Range requests (video seek, PDF page fetch).
func serveFileInline(w http.ResponseWriter, r *http.Request, full, name string) {
	if isSymlink(full) {
		writeErr(w, http.StatusForbidden, "symlinks are not allowed")
		return
	}
	f, err := openNoFollow(full)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusForbidden, "not a regular file")
		return
	}
	// Browsers fall back to download when the type is unknown; sniff a friendly
	// Content-Type for previewable extensions so <video>/<img>/pdf.js work.
	ct := sniffContentType(full, name)
	if isExecutableContentType(ct) {
		// Never hand the browser a live document on this origin. Anything that
		// could execute is downloaded instead of rendered.
		serveFileDownload(w, r, full, name)
		return
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// nosniff keeps the browser from upgrading a text/plain body to text/html by
	// content sniffing, which would reopen the XSS path the type map closes.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if needsSandbox(ct) {
		// SVG renders fine without scripting; the sandbox drops it into an opaque
		// origin so an embedded <script> cannot touch the panel's session.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	} else {
		// Set an explicit policy so the app-wide CSP (which forbids objects, to
		// harden the console itself) is not applied to a media body and does not
		// interfere with the browser's PDF/video rendering. The type here is
		// already guaranteed non-executable, so no script directive is needed.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	}
	w.Header().Set("Cache-Control", "private, max-age=0")
	// "inline" with an ASCII fallback filename so the browser tab title is sane
	// but the file is still rendered, not downloaded.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, asciiFilename(name)))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// previewableContentType maps a file extension to the MIME type the browser
// needs to render it inline. Returns "" when the extension is not previewable
// (the caller then decides whether to sniff or refuse).
var previewableContentType = map[string]string{
	// Documents
	".pdf": "application/pdf",
	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	// SVG is XML and can carry <script>. It is still previewable, but only
	// because serveFileInline sandboxes it — see executableContentType.
	".svg": "image/svg+xml",
	// Audio
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	// Video
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".m4v":  "video/x-m4v",
	".mov":  "video/quicktime",
}

// textishContentType reports a text/* Content-Type for source/config files so
// the browser renders them as text rather than offering a download.
var textishContentType = map[string]string{
	".txt":      "text/plain; charset=utf-8",
	".log":      "text/plain; charset=utf-8",
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
	".json":     "application/json; charset=utf-8",
	".csv":      "text/csv; charset=utf-8",
	".tsv":      "text/tab-separated-values; charset=utf-8",
	".ini":      "text/plain; charset=utf-8",
	".conf":     "text/plain; charset=utf-8",
	".cfg":      "text/plain; charset=utf-8",
	".yml":      "text/plain; charset=utf-8",
	".yaml":     "text/plain; charset=utf-8",
	".toml":     "text/plain; charset=utf-8",
	// Markup and script sources are shown as plain text, never as a live
	// document. Serving user-uploaded HTML as text/html on this origin is a
	// stored-XSS primitive: /api/netdisk/share/raw is public, so anyone could
	// upload a page, share the link, and run script in the panel's origin
	// against whoever opens it — reading the (deliberately non-HttpOnly) CSRF
	// cookie and acting as that user.
	".xml":       "text/plain; charset=utf-8",
	".html":      "text/plain; charset=utf-8",
	".htm":       "text/plain; charset=utf-8",
	".xhtml":     "text/plain; charset=utf-8",
	".css":       "text/plain; charset=utf-8",
	".js":        "text/plain; charset=utf-8",
	".mjs":       "text/plain; charset=utf-8",
	".ts":        "text/plain; charset=utf-8",
	".go":        "text/plain; charset=utf-8",
	".py":        "text/plain; charset=utf-8",
	".rb":        "text/plain; charset=utf-8",
	".java":      "text/plain; charset=utf-8",
	".c":         "text/plain; charset=utf-8",
	".h":         "text/plain; charset=utf-8",
	".cpp":       "text/plain; charset=utf-8",
	".hpp":       "text/plain; charset=utf-8",
	".cc":        "text/plain; charset=utf-8",
	".sh":        "text/plain; charset=utf-8",
	".bash":      "text/plain; charset=utf-8",
	".zsh":       "text/plain; charset=utf-8",
	".sql":       "text/plain; charset=utf-8",
	".env":       "text/plain; charset=utf-8",
	".gitignore": "text/plain; charset=utf-8",
}

// sniffContentType returns a browser-friendly Content-Type for previewable
// file extensions (PDF / image / audio / video / text / markdown). For unknown
// extensions it returns "" so the caller can fall back to a download.
func sniffContentType(full, name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct, ok := previewableContentType[ext]; ok {
		return ct
	}
	if ct, ok := textishContentType[ext]; ok {
		return ct
	}
	// Fall back to content sniffing only for files with no extension at all,
	// so we never override a known type with a guessed one.
	if ext == "" {
		if head, err := os.Open(full); err == nil {
			defer head.Close()
			buf := make([]byte, 512)
			n, _ := head.Read(buf)
			ct := http.DetectContentType(buf[:n])
			// DetectContentType reports text/html for an extensionless file whose
			// body looks like markup, so the text/* branch must not pass that
			// through — an upload named "readme" holding <script> would otherwise
			// be rendered as a document.
			if isExecutableContentType(ct) {
				return "text/plain; charset=utf-8"
			}
			if strings.HasPrefix(ct, "image/") ||
				strings.HasPrefix(ct, "audio/") ||
				strings.HasPrefix(ct, "video/") ||
				strings.HasPrefix(ct, "text/") {
				return ct
			}
		}
	}
	return ""
}

func serveZipDownload(w http.ResponseWriter, root, name string) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	zw := zip.NewWriter(w)
	defer zw.Close()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		// Skip symlinks to prevent archiving host files outside the root.
		if isSymlink(path) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return nil
		}
		f, err := openNoFollow(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, _ = io.Copy(fw, f)
		return nil
	})
}

func contentDisposition(name string) string {
	name = strings.ReplaceAll(filepath.Base(name), `"`, "")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiFilename(name), urlPathEscape(name))
}

func asciiFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 32 && r <= 126 && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "download"
	}
	return out
}

func urlPathEscape(s string) string {
	return url.PathEscape(s)
}

func (a *App) uniqueShareToken() (string, error) {
	for i := 0; i < 8; i++ {
		token, err := randomToken()
		if err != nil {
			return "", err
		}
		if _, err := a.db.NetdiskShare(token); err != nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("could not allocate short link")
}

// isSymlink reports whether path is a symbolic link.
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// openNoFollow opens a regular file after verifying it is not a symlink.
// This avoids following a link that escapes the netdisk root.
func openNoFollow(path string) (*os.File, error) {
	if isSymlink(path) {
		return nil, fmt.Errorf("symlink not allowed")
	}
	return os.Open(path)
}

func randomToken() (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func renderShareUnavailable(w http.ResponseWriter, expired bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title := "Link unavailable"
	msg := "This external link does not exist or has been removed."
	if expired {
		title = "Link expired"
		msg = "This external link has expired. Please ask the owner to create a new one."
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#f4f5fa;color:#1f2330;display:grid;place-items:center;min-height:100vh}.card{width:min(420px,calc(100vw - 32px));background:#fff;border:1px solid #e6e8f0;border-radius:8px;padding:24px;box-shadow:0 1px 3px rgba(16,24,40,.06)}h1{font-size:22px;margin:0 0 8px}.hint{color:#8a90a0;line-height:1.7}</style></head><body><section class="card"><h1>%s</h1><p class="hint">%s</p></section></body></html>`, title, title, msg)
}

func renderFallbackSharePage(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// This page carries its own inline script, so it needs a nonce-based policy
	// rather than the app-wide 'script-src self'. The nonce is per-response, so
	// only this exact block runs — an injected one would not carry it.
	nonce, err := randomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to render share page")
		return
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MUDP Netdisk Share</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#f4f5fa;color:#1f2330}
.wrap{max-width:920px;margin:32px auto;padding:0 18px}.card{background:#fff;border:1px solid #e6e8f0;border-radius:8px;box-shadow:0 1px 3px rgba(16,24,40,.06)}
.head{display:flex;justify-content:space-between;gap:12px;align-items:center;padding:16px;border-bottom:1px solid #e6e8f0}.body{padding:16px}
button,a.btn{display:inline-flex;align-items:center;min-height:32px;padding:0 12px;border-radius:7px;border:1px solid #e6e8f0;background:#fff;color:#1f2330;text-decoration:none;cursor:pointer}
button.primary{background:#3370ff;color:#fff;border-color:#3370ff}table{width:100%%;border-collapse:collapse}td,th{padding:10px;border-bottom:1px solid #eef0f6;text-align:left}.hint{color:#8a90a0}
</style></head><body><div class="wrap"><div class="card"><div class="head"><h2 id="title">Netdisk Share</h2><button class="primary" id="saveAll">Save to my netdisk</button></div><div class="body" id="body">Loading...</div></div></div>
<script nonce="%s">
const token=%q;
const ts=()=>Date.now();
function esc(s){return String(s||"").replace(/[&<>"']/g,m=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[m]))}
async function api(p,o){const r=await fetch(p,Object.assign({credentials:"same-origin",headers:{"Content-Type":"application/json"}},o||{}));const d=await r.json().catch(()=>({}));if(!r.ok)throw new Error(d.error||r.statusText);return d}
async function load(){try{const d=await api("/api/netdisk/share/public?token="+encodeURIComponent(token));document.getElementById("title").textContent=d.share.name+" (read only)";document.getElementById("body").innerHTML="<table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th></th></tr></thead><tbody>"+d.items.map(f=>'<tr><td>'+esc(f.name)+'</td><td>'+(f.dir?'Folder':'File')+'</td><td>'+(f.dir?'-':f.size)+'</td><td><a class=\"btn\" href=\"/api/netdisk/share/download?token='+encodeURIComponent(token)+'&path='+encodeURIComponent(f.path)+'&ts='+ts()+'\">Download</a></td></tr>').join("")+"</tbody></table>";}catch(e){document.getElementById("body").innerHTML='<p class="hint">'+esc(e.message)+'</p>'}}
document.getElementById("saveAll").onclick=async()=>{try{const d=await api("/api/netdisk/share/save",{method:"POST",body:JSON.stringify({token})});alert("Saved "+d.count+" item(s)");}catch(e){alert(e.message+"。请先登录 MUDP 后再保存。")}}
load();
</script></body></html>`, nonce, token)
}

// escapeHTMLAttr escapes a string so it can be safely placed inside a double-quoted HTML attribute.
func escapeHTMLAttr(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `"`, `&quot;`), `&`, `&amp;`)
}

type copyTask struct {
	src    string
	rel    string
	target string
	name   string
	size   int64
}

// pathSize returns the total byte size of a file or directory tree, skipping symlinks.
func pathSize(root string) int64 {
	info, err := os.Stat(root)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || isSymlink(path) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// nextFreeName returns an unused file name inside dir by appending " (1)", " (2)", etc.
func nextFreeName(dir, name string) string {
	return nextFreeNameWithPlanned(dir, name, nil)
}

// nextFreeNameWithPlanned also avoids names already reserved in planned map.
func nextFreeNameWithPlanned(dir, name string, planned map[string]bool) string {
	candidate := filepath.Join(dir, name)
	if _, exists := planned[candidate]; !exists {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		next := fmt.Sprintf("%s (%d)%s", base, i, ext)
		candidate = filepath.Join(dir, next)
		if _, exists := planned[candidate]; exists {
			continue
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return candidate
}

// nextBackupName returns a non-conflicting destination name for a backup copy.
// Unlike nextFreeName (which appends " (1)", " (2)"), it stamps the basename
// with the current date and time using dashes: "report-20260720-023015.pdf".
// If the stamped name still collides it falls back to "-2", "-3", etc. This
// makes backup history self-describing on the (slow) backup disk.
func nextBackupName(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	stamp := time.Now().Format("20060102-150405")
	stamped := fmt.Sprintf("%s-%s%s", base, stamp, ext)
	candidate = filepath.Join(dir, stamped)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	// Same-second collision (rare): append -2, -3, …
	for i := 2; i < 10000; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%s-%d%s", base, stamp, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, stamped)
}

// shareContains reports whether req is itself shared, or is a descendant of a
// shared path. It is the strict check used for downloads and saves so a folder
// shown for navigation can never be zipped wholesale when only some of its
// children were shared.
func shareContains(paths []string, req string) bool {
	req = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+req), "/"))
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+p), "/"))
		if req == p || strings.HasPrefix(req, p+"/") {
			return true
		}
	}
	return false
}

// flatShareRootItems builds the top-level listing for a share. Each shared path
// is shown by its base name so a file shared from inside a folder appears
// directly, without exposing the folder or its other contents. Shared folders
// keep dir=true so the UI renders them as navigable entries.
func flatShareRootItems(ownerRoot string, paths []string) []fileItem {
	items := make([]fileItem, 0, len(paths))
	for _, sp := range paths {
		full, _, err := cleanUserPath(ownerRoot, sp)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil || isSymlink(full) {
			continue
		}
		items = append(items, fileItem{
			Name:    filepath.Base(sp),
			Path:    filepath.ToSlash(sp),
			Dir:     info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	return items
}

// shareVisible reports whether req may be shown in a share listing. Unlike
// shareContains it also accepts an ancestor folder of a shared path: sharing
// sub/a.txt must reveal the "sub" folder so the visitor can reach the file.
// Sibling files that were never shared are never visible.
func shareVisible(paths []string, req string) bool {
	req = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+req), "/"))
	if req == "" {
		return false
	}
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+p), "/"))
		if req == p {
			return true
		}
		// req is a descendant of a shared path.
		if strings.HasPrefix(req, p+"/") {
			return true
		}
		// req is an ancestor folder that leads to a shared path.
		if strings.HasPrefix(p, req+"/") {
			return true
		}
	}
	return false
}

func copyPathWithPolicy(src, dst, policy string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			// Do not follow symlinks; skip them instead of copying the target.
			if isSymlink(path) {
				return nil
			}
			rel, _ := filepath.Rel(src, path)
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0750)
			}
			if policy == "skip" {
				if _, err := os.Stat(target); err == nil {
					return nil
				}
			}
			return copyFile(path, target)
		})
	}
	if isSymlink(src) {
		return fmt.Errorf("cannot copy symlink")
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	in, err := openNoFollow(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
