package dockerx

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
)

// FileEntry describes a single file or directory inside a container, mirroring
// the server.fileItem shape so the frontend can render container and netdisk
// listings with the same code.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"-"`
}

// cleanContainerPath normalises an absolute path inside a container. It always
// returns an absolute, slash-separated path with no ".." segments. Container
// paths are scoped by the container itself, so there is no host traversal risk;
// this just keeps the input sane for the Docker API and for tar entry joining.
func cleanContainerPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	// path.Clean("/") == "/", path.Clean("/a/../b") == "/b" — both fine.
	return cleaned, nil
}

// ContainerFileList enumerates the direct children of dir inside the container.
// It works whether the container is running or stopped, because it reads the
// container's writable layer via the Docker archive API (a tar of the directory)
// rather than exec-ing into it.
func (d *Client) ContainerFileList(ctx context.Context, id, dir string) ([]FileEntry, error) {
	dir, err := cleanContainerPath(dir)
	if err != nil {
		return nil, err
	}
	rc, _, err := d.c.CopyFromContainer(ctx, id, dir)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	// The archive of a directory contains one entry per child (the dir itself
	// is the first entry). Collect everything except the directory header.
	var out []FileEntry
	first := true
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if first {
			// First entry is the requested directory itself; skip it.
			first = false
			if hdr.Typeflag == tar.TypeDir {
				continue
			}
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || name == "." || name == ".." {
			continue
		}
		out = append(out, FileEntry{
			Name:    name,
			Path:    path.Join(dir, name),
			Dir:     hdr.Typeflag == tar.TypeDir,
			Size:    hdr.Size,
			ModTime: hdr.ModTime,
		})
	}
	return out, nil
}

// ContainerFileStat returns metadata for a single path inside the container.
func (d *Client) ContainerFileStat(ctx context.Context, id, p string) (container.PathStat, error) {
	p, err := cleanContainerPath(p)
	if err != nil {
		return container.PathStat{}, err
	}
	return d.c.ContainerStatPath(ctx, id, p)
}

// ContainerFileDownload opens the Docker archive stream for path inside the
// container. The returned reader is a tar containing the path (a single file's
// content, or a directory tree). The caller must close the reader. stat holds
// metadata for the requested path (use stat.Mode.IsDir() to decide file vs zip).
func (d *Client) ContainerFileDownload(ctx context.Context, id, p string) (io.ReadCloser, container.PathStat, error) {
	p, err := cleanContainerPath(p)
	if err != nil {
		return nil, container.PathStat{}, err
	}
	return d.c.CopyFromContainer(ctx, id, p)
}

// ContainerFileUpload copies a pre-built tar stream into destDir inside the
// container. The tar's entries are written under destDir. Works on stopped
// containers, so files can be staged before first start (like the bootstrap
// injection at create time).
func (d *Client) ContainerFileUpload(ctx context.Context, id, destDir string, tarStream io.Reader) error {
	destDir, err := cleanContainerPath(destDir)
	if err != nil {
		return err
	}
	return d.c.CopyToContainer(ctx, id, destDir, tarStream, container.CopyToContainerOptions{})
}

// assertContainerPath is a guard used by handlers to refuse obviously bad input
// early; the Docker API itself rejects paths it cannot resolve.
var errBadContainerPath = fmt.Errorf("invalid container path")
