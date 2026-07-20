package server

import (
	"archive/zip"
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
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
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid path")
	}
	return fullAbs, rel, nil
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
	if err := r.ParseMultipartForm(2 << 30); err != nil {
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

	// Pre-compute how much additional space is required, accounting for
	// partially-uploaded files that may be resumed.
	used := dirSize(root)
	var additional int64
	projected := used
	for _, fh := range files {
		dstPath, _, err := cleanUserPath(dir, fh.Filename)
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

	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dstPath, _, err := cleanUserPath(dir, fh.Filename)
		if err != nil {
			_ = src.Close()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0750); err != nil {
			_ = src.Close()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		offset := int64(0)
		if existing, err := os.Stat(dstPath); err == nil && existing.Size() < fh.Size {
			offset = existing.Size()
		}
		flags := os.O_CREATE | os.O_WRONLY
		if seeker, ok := src.(io.Seeker); ok && offset > 0 {
			_, _ = seeker.Seek(offset, io.SeekStart)
		} else {
			// Source cannot resume from an offset: truncate any partial file so
			// the new upload does not leave stale tail bytes behind.
			offset = 0
			flags |= os.O_TRUNC
		}
		dst, err := os.OpenFile(dstPath, flags, 0640)
		if err != nil {
			_ = src.Close()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_, _ = dst.Seek(offset, io.SeekStart)
		// Truncate to the resume offset so any stale tail from a previous
		// interrupted upload is removed.
		_ = dst.Truncate(offset)
		_, err = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(files)})
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
		html := string(raw)
		// Inject the token into a meta tag and a global constant for the page JS.
		html = strings.Replace(html, `<meta name="share-token" content=""`, fmt.Sprintf(`<meta name="share-token" content="%s"`, escapeHTMLAttr(token)), 1)
		html = strings.Replace(html, "window.__SHARE_TOKEN__ = \"\"", fmt.Sprintf("window.__SHARE_TOKEN__ = %q", token), 1)
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
	var toCopy []copyTask
	var required int64
	plannedTargets := make(map[string]bool)
	for _, p := range req.Paths {
		if !shareContains(share.Paths, p) {
			continue
		}
		src, rel, err := cleanUserPath(ownerRoot, p)
		if err != nil {
			continue
		}
		size := pathSize(src)
		// Preserve the original hierarchy by saving under <dst>/<shareName>/<rel>.
		targetBase := filepath.Join(dstDir, share.Name, rel)
		if policy == "rename" {
			targetBase = nextFreeNameWithPlanned(filepath.Dir(targetBase), filepath.Base(targetBase), plannedTargets)
		}
		plannedTargets[targetBase] = true
		toCopy = append(toCopy, copyTask{src: src, rel: rel, target: targetBase, name: filepath.Base(rel), size: size})
		required += size
	}

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
	if ct != "" {
		w.Header().Set("Content-Type", ct)
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
	".svg":  "image/svg+xml",
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
	".txt":       "text/plain; charset=utf-8",
	".log":       "text/plain; charset=utf-8",
	".md":        "text/markdown; charset=utf-8",
	".markdown":  "text/markdown; charset=utf-8",
	".json":      "application/json; charset=utf-8",
	".csv":       "text/csv; charset=utf-8",
	".tsv":       "text/tab-separated-values; charset=utf-8",
	".ini":       "text/plain; charset=utf-8",
	".conf":      "text/plain; charset=utf-8",
	".cfg":       "text/plain; charset=utf-8",
	".yml":       "text/plain; charset=utf-8",
	".yaml":      "text/plain; charset=utf-8",
	".toml":      "text/plain; charset=utf-8",
	".xml":       "text/xml; charset=utf-8",
	".html":      "text/html; charset=utf-8",
	".htm":       "text/html; charset=utf-8",
	".css":       "text/css; charset=utf-8",
	".js":        "text/javascript; charset=utf-8",
	".mjs":       "text/javascript; charset=utf-8",
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
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MUDP Netdisk Share</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#f4f5fa;color:#1f2330}
.wrap{max-width:920px;margin:32px auto;padding:0 18px}.card{background:#fff;border:1px solid #e6e8f0;border-radius:8px;box-shadow:0 1px 3px rgba(16,24,40,.06)}
.head{display:flex;justify-content:space-between;gap:12px;align-items:center;padding:16px;border-bottom:1px solid #e6e8f0}.body{padding:16px}
button,a.btn{display:inline-flex;align-items:center;min-height:32px;padding:0 12px;border-radius:7px;border:1px solid #e6e8f0;background:#fff;color:#1f2330;text-decoration:none;cursor:pointer}
button.primary{background:#3370ff;color:#fff;border-color:#3370ff}table{width:100%%;border-collapse:collapse}td,th{padding:10px;border-bottom:1px solid #eef0f6;text-align:left}.hint{color:#8a90a0}
</style></head><body><div class="wrap"><div class="card"><div class="head"><h2 id="title">Netdisk Share</h2><button class="primary" id="saveAll">Save to my netdisk</button></div><div class="body" id="body">Loading...</div></div></div>
<script>
const token=%q;
const ts=()=>Date.now();
function esc(s){return String(s||"").replace(/[&<>"']/g,m=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[m]))}
async function api(p,o){const r=await fetch(p,Object.assign({credentials:"same-origin",headers:{"Content-Type":"application/json"}},o||{}));const d=await r.json().catch(()=>({}));if(!r.ok)throw new Error(d.error||r.statusText);return d}
async function load(){try{const d=await api("/api/netdisk/share/public?token="+encodeURIComponent(token));document.getElementById("title").textContent=d.share.name+" (read only)";document.getElementById("body").innerHTML="<table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th></th></tr></thead><tbody>"+d.items.map(f=>'<tr><td>'+esc(f.name)+'</td><td>'+(f.dir?'Folder':'File')+'</td><td>'+(f.dir?'-':f.size)+'</td><td><a class=\"btn\" href=\"/api/netdisk/share/download?token='+encodeURIComponent(token)+'&path='+encodeURIComponent(f.path)+'&ts='+ts()+'\">Download</a></td></tr>').join("")+"</tbody></table>";}catch(e){document.getElementById("body").innerHTML='<p class="hint">'+esc(e.message)+'</p>'}}
document.getElementById("saveAll").onclick=async()=>{try{const d=await api("/api/netdisk/share/save",{method:"POST",body:JSON.stringify({token})});alert("Saved "+d.count+" item(s)");}catch(e){alert(e.message+"。请先登录 MUDP 后再保存。")}}
load();
</script></body></html>`, token)
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

func copyPath(src, dst string) error {
	return copyPathWithPolicy(src, dst, "overwrite")
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
