package server

import (
	"archive/tar"
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"mudp/internal/store"
)

// containerFilesList enumerates the children of a directory inside a container.
// Running containers are listed via a fast exec path; stopped containers fall back
// to the slower Docker archive API.
// GET /api/containers/files/list?id=&path=
func (a *App) containerFilesList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot browse files")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	p := r.URL.Query().Get("path")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, id) {
		writeErr(w, http.StatusForbidden, "container not found")
		return
	}
	entries, err := a.docker.ContainerFileList(r.Context(), id, p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]fileItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, fileItem{
			Name: e.Name, Path: e.Path, Dir: e.Dir, Size: e.Size, Mode: e.Mode,
			ModTime: e.ModTime.Format(time.RFC3339),
		})
	}
	dir := cleanDisplayContainerPath(p)
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "items": items})
}

// containerFilesDownload streams a single file (raw) or a folder (zipped) from a
// container to the client.
// GET /api/containers/files/download?id=&path=
func (a *App) containerFilesDownload(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot download files")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	p := r.URL.Query().Get("path")
	if id == "" || p == "" {
		writeErr(w, http.StatusBadRequest, "id and path are required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, id) {
		writeErr(w, http.StatusForbidden, "container not found")
		return
	}
	rc, stat, err := a.docker.ContainerFileDownload(r.Context(), id, p)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	defer rc.Close()
	if !stat.Mode.IsDir() {
		// Single file: pull its bytes out of the (single-entry) tar stream.
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Content-Disposition", contentDisposition(stat.Name))
		_ = streamTarFile(w, rc, stat.Name)
		return
	}
	// Directory: stream a zip of its contents, skipping symlinks.
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(stat.Name+".zip"))
	streamContainerTarAsZip(w, rc)
}

// streamTarFile copies the named member out of the tar stream to w. The Docker
// archive of a single file contains exactly one entry holding its bytes; the
// match is on the entry NAME only — never on "it's a regular file", which
// would let a crafted archive serve some other member's bytes under the
// expected download filename (docs/SECURITY-AUDIT.md L-2).
func streamTarFile(w io.Writer, rc io.Reader, name string) error {
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return err
		}
		if path.Base(strings.TrimSuffix(hdr.Name, "/")) == name && hdr.Typeflag == tar.TypeReg {
			// Cap the copy at hdr.Size (authoritative for tar regular files)
			// so a forged/crafted archive cannot stream unbounded bytes and
			// exhaust server memory (decompression-bomb, CWE-409).
			_, err = io.Copy(w, io.LimitReader(tr, hdr.Size))
			return err
		}
	}
}

// streamContainerTarAsZip re-archives the container tar stream into a zip on the
// fly, skipping symlink/hardlink entries (which could point outside the tree).
func streamContainerTarAsZip(w io.Writer, rc io.Reader) {
	zw := zip.NewWriter(w)
	defer zw.Close()
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "/")
		if name == "" {
			continue
		}
		// Member-name containment (docs/SECURITY-AUDIT.md L-3): a hostile
		// container can emit entries with traversal-shaped names, which would
		// land in the zip verbatim and escape the extraction root on the
		// DOWNLOADING user's machine (client-side ZipSlip). Same guard style
		// as extractContainerTar below — anything that cleans to outside the
		// archive root is dropped.
		cleaned := path.Clean(name)
		if cleaned == ".." || cleaned == "." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
			continue
		}
		name = cleaned
		fi := hdr.FileInfo()
		fh, err := zw.CreateHeader(&zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: hdr.ModTime,
		})
		if err != nil {
			return
		}
		if !fi.IsDir() {
			// Cap each member at its declared size (decompression-bomb defense).
			_, _ = io.Copy(fh, io.LimitReader(tr, hdr.Size))
		}
	}
}

// containerCopyRequest is the body of POST /api/containers/files/copy.
type containerCopyRequest struct {
	ID        string   `json:"id"`
	Direction string   `json:"direction"` // "to-container" | "to-netdisk"
	Paths     []string `json:"paths"`     // source paths on the FROM side
	DestDir   string   `json:"destDir"`   // destination dir on the TO side
}

// containerFilesCopy copies selected paths between the user's netdisk and a
// container they own, in either direction. Folders are copied recursively.
// Symlinks are skipped on both sides to prevent escaping the netdisk root.
// POST /api/containers/files/copy
func (a *App) containerFilesCopy(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot copy files")
		return
	}
	var req containerCopyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "id and paths are required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ID) {
		writeErr(w, http.StatusForbidden, "container not found")
		return
	}
	var (
		copied int
		err    error
	)
	switch req.Direction {
	case "to-container":
		copied, err = a.copyNetdiskToContainer(r.Context(), u, req)
	case "to-netdisk":
		copied, err = a.copyContainerToNetdisk(r.Context(), u, req)
	default:
		writeErr(w, http.StatusBadRequest, "direction must be to-container or to-netdisk")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "copied": copied})
}

// copyNetdiskToContainer tars each selected netdisk path (file or folder,
// recursively) and streams it into req.DestDir inside the container.
func (a *App) copyNetdiskToContainer(ctx context.Context, u *store.User, req containerCopyRequest) (int, error) {
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		return 0, err
	}
	destDir := cleanDisplayContainerPath(req.DestDir)
	if st, err := a.docker.ContainerFileStat(ctx, req.ID, destDir); err != nil {
		return 0, err
	} else if !st.Mode.IsDir() {
		return 0, fmt.Errorf("destination %s is not a directory", destDir)
	}
	copied := 0
	for _, src := range req.Paths {
		full, _, err := cleanUserPath(root, src)
		if err != nil {
			return copied, err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return copied, err
		}
		if isSymlink(full) {
			continue // never copy symlinks out of the netdisk root
		}
		// Build an in-memory tar for this entry and push it into the container.
		pr, pw := io.Pipe()
		go func() {
			err := tarHostPath(pw, full, info)
			_ = pw.CloseWithError(err)
		}()
		if err := a.docker.ContainerFileUpload(ctx, req.ID, destDir, pr); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

func cleanDisplayContainerPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// tarHostPath writes a tar of the host path `full` (file or directory, walked
// recursively) to w. The archive's entry names are relative to the path's own
// name so that CopyToContainer drops it under the container destDir cleanly.
// Symlinks are skipped to avoid bundling host files outside the netdisk root.
func tarHostPath(w io.Writer, full string, info os.FileInfo) error {
	tw := tar.NewWriter(w)
	defer tw.Close()
	base := filepath.Base(full)
	write := func(p string, fi os.FileInfo) error {
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(full, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			hdr.Name = base
		} else {
			hdr.Name = path.Join(base, rel)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil // skip
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	}
	if !info.IsDir() {
		return write(full, info)
	}
	return filepath.Walk(full, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if isSymlink(p) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return write(p, fi)
	})
}

// copyContainerToNetdisk pulls each selected container path as a tar and
// extracts it under the user's netdisk DestDir. Symlink entries are skipped so a
// malicious container cannot plant links outside the netdisk root.
func (a *App) copyContainerToNetdisk(ctx context.Context, u *store.User, req containerCopyRequest) (int, error) {
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		return 0, err
	}
	destFull, _, err := cleanUserPath(root, req.DestDir)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(destFull, 0750); err != nil {
		return 0, err
	}
	copied := 0
	for _, src := range req.Paths {
		rc, _, err := a.docker.ContainerFileDownload(ctx, req.ID, src)
		if err != nil {
			return copied, err
		}
		n, err := extractContainerTar(rc, destFull)
		rc.Close()
		if err != nil {
			return copied, err
		}
		copied += n
	}
	return copied, nil
}

// extractContainerTar writes the entries of a container archive (tar) into dest
// on the host, skipping symlink/hardlink entries and refusing paths that would
// escape dest via "..". Returns the count of regular files + directories
// written, so the caller can report how much was copied.
func extractContainerTar(rc io.Reader, dest string) (int, error) {
	tr := tar.NewReader(rc)
	written := 0
	cleanDest, err := filepath.Abs(filepath.Clean(dest))
	if err != nil {
		return 0, err
	}
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return written, err
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue
		}
		// Strip any leading "./" or "/" so names join cleanly under dest.
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		target := filepath.Join(cleanDest, filepath.FromSlash(name))
		targetAbs, err := filepath.Abs(filepath.Clean(target))
		if err != nil {
			return written, err
		}
		// Path-traversal guard: the resolved target must stay under dest.
		if targetAbs != cleanDest && !strings.HasPrefix(targetAbs, cleanDest+string(filepath.Separator)) {
			continue
		}
		fi := hdr.FileInfo()
		if fi.IsDir() {
			if err := os.MkdirAll(targetAbs, fi.Mode().Perm()); err != nil {
				return written, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0750); err != nil {
			return written, err
		}
		f, err := os.OpenFile(targetAbs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return written, err
		}
		// Cap extraction at hdr.Size (authoritative for tar.TypeReg) to
		// defend against decompression bombs written to the netdisk (CWE-409).
		// hdr.Size < 0 only happens with a malformed archive; skip such entries.
		if hdr.Size < 0 {
			f.Close()
			continue
		}
		if _, err := io.Copy(f, io.LimitReader(tr, hdr.Size)); err != nil {
			f.Close()
			return written, err
		}
		f.Close()
		written++
	}
	return written, nil
}
