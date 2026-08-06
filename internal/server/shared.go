package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mudp/internal/store"

	"mudp/internal/dockerx"
)

// The shared disk (共享盘) is one pool per group, configured by an admin the
// same way as the group's netdisk/backup roots (see groupSharedDisk in
// disks.go). It is a distinct feature from the netdisk's external share
// links (分享链接, netdiskShare* in netdisk.go / /api/netdisk/share*) — the
// "SharedDisk"/"shareddisk" naming below is kept deliberately different from
// "Share"/"share" throughout this codebase so the two are never confused in
// code, routes, or user-facing strings.
//
// Every member of the group gets a subfolder inside the pool named from
// their pinyin-slugified display name, e.g. "zhangsan-3". Anyone in the
// group can browse the whole tree, but only the owner of a subfolder (or an
// admin, who can manage everyone's) may create/rename/delete inside it —
// enforced by requireOwnSharedDiskPath on every mutating handler below.
// Getting files in or out of the pool goes through the existing
// netdisk↔disk transfer endpoint (netdiskTransfer in backup.go,
// fromDisk/toDisk "shareddisk") rather than a dedicated upload/download
// surface here — the shared disk itself only needs browse + basic in-place
// file management (mkdir/delete/rename) and a capacity readout.

// sharedDiskGroup resolves which group's shared pool u browses and mounts,
// returning that group's id alongside its root path. Normally this is the
// caller's own group. An admin whose groups have no shared disk configured
// falls back to the first group that does: admins may already create, rename
// and delete anywhere inside a pool (requireOwnSharedDiskPath) and manage the
// group paths themselves, so refusing them the pool merely because they were
// never added to that group would leave the feature unreachable for the one
// role meant to administer it. A zero id means nothing is configured
// anywhere.
func (a *App) sharedDiskGroup(u *store.User) (int64, string, error) {
	gid, root, err := a.db.SharedDiskGroupForUser(u.ID)
	if err != nil {
		return 0, "", err
	}
	if root == "" && u.Role == store.RoleAdmin {
		return a.db.AnySharedDiskGroup()
	}
	return gid, root, nil
}

// sharedDiskGroupRoot resolves the group-configured shared-disk root for u,
// without creating anything. Callers that need the caller's own subfolder to
// exist should use userSharedDiskOwnRoot instead.
func (a *App) sharedDiskGroupRoot(u *store.User) (string, error) {
	_, root, err := a.sharedDiskGroup(u)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("shared disk path is not configured for your group")
	}
	return root, nil
}

// userSharedDiskOwnRoot resolves and creates the caller's own subfolder
// inside their group's shared pool, mirroring ensureUserNetdisk (including
// the ACL fix, since a container mounted read-write into this folder can
// write into it as root just like the netdisk mount does).
func (a *App) userSharedDiskOwnRoot(u *store.User) (string, error) {
	root, err := a.sharedDiskGroupRoot(u)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, pinyinOwnerDirName(u))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	fixNetdiskACL(dir)
	return dir, nil
}

// sharedDiskRootEnsured resolves the group's shared root for browsing and
// makes sure the caller's own subfolder exists within it, so a user always
// sees their own (empty) folder the first time they open the shared disk.
func (a *App) sharedDiskRootEnsured(u *store.User) (string, error) {
	if _, err := a.userSharedDiskOwnRoot(u); err != nil {
		return "", err
	}
	return a.sharedDiskGroupRoot(u)
}

// firstPathSegment returns the first path component of a cleaned relative
// path ("" for the root itself).
func firstPathSegment(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	if rel == "." || rel == "" {
		return ""
	}
	if before, _, ok := strings.Cut(rel, "/"); ok {
		return before
	}
	return rel
}

// requireOwnSharedDiskPath rejects any mutation outside the caller's own
// subfolder — including the shared root itself (first segment ""). Admins
// are exempt: they can create/rename/delete anywhere in the pool, so they
// can manage a departed or misbehaving user's shared files.
func requireOwnSharedDiskPath(u *store.User, rel string) error {
	if u.Role == store.RoleAdmin {
		return nil
	}
	if firstPathSegment(rel) != pinyinOwnerDirName(u) {
		return fmt.Errorf("you can only modify files inside your own shared folder")
	}
	return nil
}

func (a *App) sharedDiskBrowse(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	root, err := a.sharedDiskRootEnsured(u)
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
	own := pinyinOwnerDirName(u)
	items := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
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
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(rel), "items": items, "ownFolder": own})
}

func (a *App) sharedDiskMkdir(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify the shared disk")
		return
	}
	root, err := a.sharedDiskGroupRoot(u)
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
	full, rel, err := cleanUserPath(root, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := requireOwnSharedDiskPath(u, rel); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.MkdirAll(full, 0750))
}

func (a *App) sharedDiskDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify the shared disk")
		return
	}
	root, err := a.sharedDiskGroupRoot(u)
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
		full, rel, err := cleanUserPath(root, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := requireOwnSharedDiskPath(u, rel); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := os.RemoveAll(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sharedDiskRename renames or moves a file/folder within the shared pool.
// For a regular user both endpoints must resolve inside their own
// subfolder; an admin may rename or move anything, including relocating a
// file between two different users' folders.
func (a *App) sharedDiskRename(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify the shared disk")
		return
	}
	root, err := a.sharedDiskGroupRoot(u)
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
	from, fromRel, err := cleanUserPath(root, req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := requireOwnSharedDiskPath(u, fromRel); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	to, toRel, err := cleanUserPath(root, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := requireOwnSharedDiskPath(u, toRel); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.Rename(from, to))
}

// sharedDiskQuota reports the shared pool's total usage (everyone's
// subfolders combined) and the disk's free space, mirroring netdiskQuota.
// There is no per-user quota field for the shared disk, so no
// totalBytes/freeBytes pair is reported — only what's used and what's free
// on the underlying disk.
func (a *App) sharedDiskQuota(w http.ResponseWriter, r *http.Request) {
	root, err := a.sharedDiskGroupRoot(currentUser(r))
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
	if free, err := diskFree(root); err == nil && free >= 0 {
		q["diskFreeBytes"] = free
	}
	writeJSON(w, http.StatusOK, q)
}

// sharedDiskAccessSettings handles GET/POST for the caller's own shared-disk
// read-write preference (see User.SharedDiskReadWrite), mirroring
// userLanguage in language.go.
func (a *App) sharedDiskAccessSettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]bool{"readWrite": u.SharedDiskReadWrite})
	case http.MethodPost:
		if !canMutate(u) {
			writeErr(w, http.StatusForbidden, "read-only role cannot change this setting")
			return
		}
		var req struct {
			ReadWrite bool `json:"readWrite"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.db.UpdateUserSharedDiskReadWrite(u.ID, req.ReadWrite); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.record(r, "shareddisk.access", fmt.Sprintf("readWrite=%v", req.ReadWrite))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "readWrite": req.ReadWrite})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// sharedDiskMountsFor returns the shared-disk (共享盘) bind-mount specs for a
// container created with the shared disk bound in. It uses a two-layer
// structure so that a container reflects not just the members present at
// create time but also any added later:
//
//	-v /pool:/data:ro                  ← whole pool, read-only base
//	-v /pool/userme:/data/userme:rw    ← only folders that must be writable
//
// The first spec (Name == "") binds the entire pool root read-only at /data.
// Docker does not allow adding or removing bind mounts after a container is
// created, so without this base mount a container built today would never
// see a subfolder added tomorrow (a new member joining the group). Binding
// the whole pool once means every new subfolder — created automatically by
// sharedDiskRootEnsured when a member first opens the shared disk — shows
// up in every already-running container's /data, read-only by default.
//
// Folders that stay read-only need no mount of their own — the base already
// covers them — so they are skipped. A folder only gets its own mount when
// it must be read-write, overlaying the base to grant writes. Whether a
// folder is writable is decided by its OWNER's own SharedDiskReadWrite
// preference (see UpdateUserSharedDiskReadWrite) — not by who is creating
// this container. A folder the owner has kept read-only stays read-only
// even in someone else's container; one they opted into read-write is
// writable everywhere the shared disk is mounted. There are two exceptions:
// a user's own folder is always read-write in their own container
// regardless of their preference (the preference only governs how OTHER
// members' containers mount it), and an admin's own container gets
// read-write on every folder, overriding each owner's preference, so an
// admin can always fix up or clean up anyone's shared files from inside a
// container too.
//
// The base mount must precede the per-folder ones in the returned slice so
// Docker applies the broader read-only view first and the narrower
// read-write overrides on top of it.
func (a *App) sharedDiskMountsFor(u *store.User) ([]dockerx.SharedDiskMount, error) {
	own, err := a.userSharedDiskOwnRoot(u)
	if err != nil {
		return nil, err
	}
	gid, root, err := a.sharedDiskGroup(u)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, fmt.Errorf("shared disk path is not configured for your group")
	}
	ownName := pinyinOwnerDirName(u)
	// Members of the group that owns the pool, not of the caller's own group —
	// the two differ for an admin resolving through the fallback above.
	members, err := a.db.SharedDiskGroupMembers(gid)
	if err != nil {
		return nil, err
	}
	readWriteByFolder := make(map[string]bool, len(members))
	for _, m := range members {
		readWriteByFolder[pinyinOwnerDirName(&m)] = m.SharedDiskReadWrite
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	isAdminUser := u.Role == store.RoleAdmin
	// The whole-pool read-only base mount (appended below) already makes every
	// subfolder read-only. So a subfolder only needs its OWN mount when it must
	// be read-write — overlaying the base to grant writes. Folders that stay
	// read-only are covered by the base and are skipped here, keeping the mount
	// list (and thus Docker's frozen HostConfig) to a minimum.
	mounts := make([]dockerx.SharedDiskMount, 0, len(entries)+1)
	mounts = append(mounts, dockerx.SharedDiskMount{Source: root, Name: "", ReadOnly: true})
	for _, entry := range entries {
		if !entry.IsDir() || isSymlink(filepath.Join(root, entry.Name())) {
			continue
		}
		name := entry.Name()
		// Writable when: it's the caller's own folder, an admin's container
		// (admin can write everywhere), or the owner opted into read-write.
		if !(name == ownName || isAdminUser || readWriteByFolder[name]) {
			continue
		}
		source := filepath.Join(root, name)
		if name == ownName {
			source = own
		}
		mounts = append(mounts, dockerx.SharedDiskMount{Source: source, Name: name, ReadOnly: false})
	}
	return mounts, nil
}
