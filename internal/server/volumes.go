package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/httpx"
)

// volumes handles GET (list) and POST (create) for mudp-managed volumes.
func (a *App) volumes(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.docker.ListVolumes(r.Context(), u.Username, u.Role == "admin")
		if err != nil {
			httpx.Logger(r).Error("list volumes failed", "error", err)
			if dockerx.IsUnavailableError(err) {
				writeErr(w, http.StatusServiceUnavailable, "docker unavailable")
				return
			}
			respond(w, items, err)
			return
		}
		respond(w, items, nil)
	case http.MethodPost:
		if !canMutate(u) {
			writeErr(w, http.StatusForbidden, "read-only role cannot create volumes")
			return
		}
		var req struct {
			Name       string            `json:"name"`
			Driver     string            `json:"driver"`
			DriverOpts map[string]string `json:"driverOpts"`
			Labels     map[string]string `json:"labels"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, "name is required")
			return
		}
		full, err := a.docker.CreateVolume(r.Context(), dockerx.CreateVolumeOptions{
			Username:   u.Username,
			Name:       req.Name,
			Driver:     req.Driver,
			DriverOpts: req.DriverOpts,
			Labels:     req.Labels,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.record(r, "volume.create", full)
		writeJSON(w, http.StatusOK, map[string]string{"name": full})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// volumeDelete removes a single volume (ownership-guarded server-side).
func (a *App) volumeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot delete volumes")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Force bool   `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	name := req.Name
	if !strings.HasPrefix(name, dockerx.Prefix) {
		// Backward compatibility: the UI used to send the display name.
		// Resolve it using the caller's username; admins deleting another
		// user's volume must send the full Docker name.
		name = dockerx.VolumeFullName(u.Username, name)
	}
	if err := a.docker.RemoveVolume(r.Context(), name, u.Username, u.Role == "admin", req.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "volume.delete", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// volumePrune removes unused dangling volumes owned by the caller (admin: all).
func (a *App) volumePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot prune volumes")
		return
	}
	count, bytes, err := a.docker.PruneVolumes(r.Context(), u.Username, u.Role == "admin")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "volume.prune", targetName(u))
	writeJSON(w, http.StatusOK, map[string]any{"removed": count, "bytesFreed": bytes})
}

// resolveVolumeMount resolves a volume name to its host mountpoint and enforces
// ownership. The returned mountpoint is the "root" for the volume file browser;
// all relative paths are cleaned against it via cleanUserPath (the netdisk path
// sanitizer) to prevent traversal outside the volume.
func (a *App) resolveVolumeMount(r *http.Request, raw string) (string, string, error) {
	u := currentUser(r)
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "", errors.New("name is required")
	}
	if !strings.HasPrefix(name, dockerx.Prefix) {
		// Backward compat: the UI sends display names; resolve to the caller's
		// full volume name. Admins browsing another user's volume must send the
		// full Docker name.
		name = dockerx.VolumeFullName(u.Username, name)
	}
	vol, err := a.docker.VolumeInspect(r.Context(), name)
	if err != nil {
		return "", "", err
	}
	if vol.Labels[dockerx.ManagedLabel] != "true" {
		return "", "", errors.New("volume is not managed by mudp")
	}
	if u.Role != "admin" && vol.Labels[dockerx.UserLabel] != u.Username {
		return "", "", errors.New("volume is not yours")
	}
	if vol.Mountpoint == "" {
		return "", "", errors.New("volume has no mountpoint")
	}
	return vol.Mountpoint, name, nil
}

// volumeFilesList lists the contents of a directory inside a volume. The path
// is cleaned against the volume mountpoint to prevent host traversal.
func (a *App) volumeFilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, rel, err := cleanUserPath(mount, r.URL.Query().Get("path"))
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
		// Lstat so symlinks aren't followed; skip them entirely (matches netdisk).
		li, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if rel == "." {
			p = entry.Name()
		}
		items = append(items, fileItem{
			Name:    entry.Name(),
			Path:    p,
			Dir:     entry.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(rel), "items": items})
}

// volumeFilesDownload serves a single file or a zipped directory from a volume.
func (a *App) volumeFilesDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(mount, r.URL.Query().Get("path"))
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

// volumeFilesMkdir creates a directory inside a volume. Mutating.
func (a *App) volumeFilesMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name and path are required")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(mount, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.MkdirAll(full, 0750))
}

// volumeFilesDelete removes files/dirs inside a volume. Mutating.
func (a *App) volumeFilesDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	var req struct {
		Name  string   `json:"name"`
		Paths []string `json:"paths"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "name and paths are required")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, p := range req.Paths {
		full, _, err := cleanUserPath(mount, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.RemoveAll(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	a.record(r, "volume.files.delete", req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// volumeFilesRename renames a file/dir inside a volume. Mutating.
func (a *App) volumeFilesRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.NewName) == "" {
		writeErr(w, http.StatusBadRequest, "name, path and newName are required")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(mount, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// New name must be a bare filename (no path separators) to stay in the same dir.
	newBase := filepath.Base(strings.TrimSpace(req.NewName))
	dst, _, err := cleanUserPath(mount, filepath.Join(filepath.Dir(req.Path), newBase))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Rename(full, dst); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "volume.files.rename", req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// volumeFilesUpload handles multipart uploads into a volume directory, with the
// same interrupted-upload resume behavior as netdisk. Mutating.
func (a *App) volumeFilesUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
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
	mount, _, err := a.resolveVolumeMount(r, r.FormValue("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(mount, r.FormValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	// Per-file CRC32 digests the client computed (parallel to "files"). Absent for
	// legacy clients: files are still written + checksummed, just not compared.
	hashes := r.MultipartForm.Value["hashes"]
	var results []uploadResult
	for i, fh := range files {
		src, err := fh.Open()
		if err != nil {
			// A single unreadable part no longer aborts the whole batch.
			results = append(results, uploadResult{Path: filepath.Base(fh.Filename), Error: err.Error()})
			continue
		}
		dstPath, _, err := cleanUserPath(dir, filepath.Base(fh.Filename))
		if err != nil {
			_ = src.Close()
			results = append(results, uploadResult{Path: filepath.Base(fh.Filename), Error: err.Error()})
			continue
		}
		expected := ""
		if i < len(hashes) {
			expected = hashes[i]
		}
		sum, werr := writeFileWithCRC32(dstPath, src, fh, expected)
		_ = src.Close()
		if werr != nil {
			results = append(results, uploadResult{Path: filepath.Base(fh.Filename), CRC32: sum, Error: werr.Error()})
		} else {
			results = append(results, uploadResult{Path: filepath.Base(fh.Filename), CRC32: sum})
		}
	}
	okCount := len(files) - countFailedResults(results)
	a.record(r, "volume.files.upload", r.FormValue("name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": okCount == len(files), "count": len(files), "results": results})
}

// ---- Large-file chunked/resumable upload (volumes) --------------------------
// Mirrors the netdisk chunk handlers but resolves a Docker volume mountpoint
// instead of a user netdisk root, and applies no quota (volumes are unbounded).

func (a *App) volumeChunkInit(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	// The volume identifier travels as a URL query parameter (?name=), distinct
	// from the JSON body's "name" field, which is the in-volume file name. The
	// query string is always available via r.URL regardless of the JSON body,
	// so this mirrors volumeFilesList/Download's ?name= convention.
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req chunkInitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(mount, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunkInit(w, dir, req.Name, req, nil)
}

func (a *App) volumeChunk(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	// Volume identifier is a URL query parameter (?name=); the multipart form's
	// "name" field is the in-volume file name, consumed by handleChunk below.
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(mount, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunk(w, r, dir)
}

func (a *App) volumeChunkComplete(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	// Volume identifier is a URL query parameter (?name=); the JSON body's
	// "name" field is the in-volume file name, consumed by handleChunkComplete.
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(mount, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunkComplete(w, r, dir)
}

func (a *App) volumeChunkAbort(w http.ResponseWriter, r *http.Request) {
	if !canMutate(currentUser(r)) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify volumes")
		return
	}
	mount, _, err := a.resolveVolumeMount(r, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(mount, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleChunkAbort(w, r, dir)
}
