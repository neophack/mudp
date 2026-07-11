package dockerx

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

// FileEntry describes a single file or directory inside a container, mirroring
// the server.fileItem shape so the frontend can render container and netdisk
// listings with the same code.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
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
// For running containers it uses a lightweight exec (`find` + `stat`) so only
// the requested directory is scanned. For stopped containers at "/" it avoids
// archiving the whole filesystem by stat-ing likely root children. Other stopped
// container directories fall back to the Docker archive API, which is slower
// because Docker returns the whole subtree for the requested directory.
func (d *Client) ContainerFileList(ctx context.Context, id, dir string) ([]FileEntry, error) {
	dir, err := cleanContainerPath(dir)
	if err != nil {
		return nil, err
	}
	info, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}

	// Fast path: list directly inside a running container.
	if info.State.Running {
		if entries, err := d.containerFileListExec(ctx, id, dir); err == nil {
			return entries, nil
		}
	}

	if !info.State.Running && dir == "/" {
		return d.containerRootListStopped(ctx, id, info.Config.WorkingDir, mountDestinations(info.Mounts))
	}

	// Fallback: archive API works on stopped containers too.
	return d.containerFileListArchive(ctx, id, dir)
}

// containerFileListExec lists direct children of dir by exec-ing find+stat.
// It requires the container to be running and to have /bin/sh, find and stat.
func (d *Client) containerFileListExec(ctx context.Context, id, dir string) ([]FileEntry, error) {
	info, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	if !info.State.Running {
		return nil, fmt.Errorf("container not running")
	}

	// stat %A = human mode, %s = size, %Y = mtime epoch, %n = full path.
	cmd := fmt.Sprintf("find %s -maxdepth 1 -mindepth 1 -exec stat -c '%%A|%%s|%%Y|%%n' {} +",
		shellQuote(dir))
	execCfg := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/sh", "-c", cmd},
	}

	resp, err := d.c.ContainerExecCreate(ctx, id, execCfg)
	if err != nil {
		return nil, err
	}
	attach, err := d.c.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{})
	if err != nil {
		return nil, err
	}
	defer attach.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return nil, err
	}

	inspect, err := d.c.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return nil, err
	}
	if inspect.ExitCode != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "exec failed"
		}
		return nil, fmt.Errorf("%s (exit %d)", msg, inspect.ExitCode)
	}

	return parseExecFileList(dir, stdout.String()), nil
}

// containerFileListArchive reads a tar archive produced by CopyFromContainer for
// dir and returns only the direct children of dir.
func (d *Client) containerFileListArchive(ctx context.Context, id, dir string) ([]FileEntry, error) {
	rc, _, err := d.c.CopyFromContainer(ctx, id, dir)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return parseContainerFileList(dir, tar.NewReader(rc))
}

func (d *Client) containerRootListStopped(ctx context.Context, id, workingDir string, mountDests []string) ([]FileEntry, error) {
	candidates := rootListCandidates(workingDir, mountDests)
	out := make([]FileEntry, 0, len(candidates))
	for _, name := range candidates {
		p := "/" + name
		st, err := d.ContainerFileStat(ctx, id, p)
		if err != nil {
			continue
		}
		entryName := st.Name
		if entryName == "" || entryName == "/" {
			entryName = name
		}
		out = append(out, FileEntry{
			Name:    entryName,
			Path:    "/" + entryName,
			Dir:     st.Mode.IsDir(),
			Size:    st.Size,
			Mode:    st.Mode.String(),
			ModTime: st.Mtime,
		})
	}
	return out, nil
}

func mountDestinations(mounts []types.MountPoint) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		if m.Destination != "" {
			out = append(out, m.Destination)
		}
	}
	return out
}

func rootListCandidates(workingDir string, extraPaths []string) []string {
	common := []string{
		"bin", "boot", "dev", "etc", "home", "lib", "lib32", "lib64", "libx32",
		"media", "mnt", "opt", "proc", "root", "run", "sbin", "srv", "sys", "tmp",
		"usr", "var", "app", "data", "workspace", "workspaces",
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(common)+len(extraPaths)+1)
	add := func(p string) {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			return
		}
		if i := strings.IndexByte(p, '/'); i >= 0 {
			p = p[:i]
		}
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range common {
		add(p)
	}
	add(workingDir)
	for _, p := range extraPaths {
		add(p)
	}
	return out
}

// parseExecFileList parses output from the find+stat exec command.
func parseExecFileList(dir, output string) []FileEntry {
	namePrefix := dir
	if namePrefix != "/" {
		namePrefix += "/"
	}

	var out []FileEntry
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		modeStr := parts[0]
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		mtimeSec, _ := strconv.ParseInt(parts[2], 10, 64)
		fullPath := parts[3]
		name := strings.TrimPrefix(fullPath, namePrefix)
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			continue
		}
		out = append(out, FileEntry{
			Name:    name,
			Path:    path.Join(dir, name),
			Dir:     strings.HasPrefix(modeStr, "d"),
			Size:    size,
			Mode:    modeStr,
			ModTime: time.Unix(mtimeSec, 0),
		})
	}
	return out
}

// shellQuote returns a single-quoted shell string literal.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseContainerFileList reads a tar archive produced by CopyFromContainer for
// dir and returns only the direct children of dir. Docker archives include all
// recursive descendants, so we filter out anything deeper than one level.
func parseContainerFileList(dir string, tr *tar.Reader) ([]FileEntry, error) {
	prefix := strings.Trim(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	var out []FileEntry
	seen := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		// Docker may prefix root entries with "./" or "/".
		name = strings.TrimPrefix(name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "." || name == ".." {
			continue
		}
		// Skip entries that are not under this directory or are nested deeper.
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		relative := strings.TrimPrefix(name, prefix)
		if strings.Contains(relative, "/") {
			continue
		}
		// The directory itself appears as an entry; skip it.
		if name == strings.TrimSuffix(prefix, "/") {
			continue
		}
		// Avoid duplicates if the same name appears with and without a trailing slash.
		if seen[relative] {
			continue
		}
		seen[relative] = true
		out = append(out, FileEntry{
			Name:    relative,
			Path:    path.Join(dir, relative),
			Dir:     hdr.Typeflag == tar.TypeDir,
			Size:    hdr.Size,
			Mode:    formatFileMode(hdr.Typeflag, hdr.Mode),
			ModTime: hdr.ModTime,
		})
	}
	return out, nil
}

// formatFileMode renders a tar mode/type as an ls-style string, e.g.
// "drwxr-xr-x" or "-rw-r--r--".
func formatFileMode(typeflag byte, mode int64) string {
	var b [10]byte
	switch typeflag {
	case tar.TypeDir:
		b[0] = 'd'
	case tar.TypeSymlink:
		b[0] = 'l'
	case tar.TypeChar:
		b[0] = 'c'
	case tar.TypeBlock:
		b[0] = 'b'
	case tar.TypeFifo:
		b[0] = 'p'
	default:
		b[0] = '-'
	}
	perms := []struct {
		bit uint32
		ch  byte
	}{
		{0400, 'r'}, {0200, 'w'}, {0100, 'x'},
		{0040, 'r'}, {0020, 'w'}, {0010, 'x'},
		{0004, 'r'}, {0002, 'w'}, {0001, 'x'},
	}
	for i, p := range perms {
		if uint32(mode)&p.bit != 0 {
			b[i+1] = p.ch
		} else {
			b[i+1] = '-'
		}
	}
	return string(b[:])
}

// ContainerFileStat returns metadata for a single path inside the container.
func (d *Client) ContainerFileStat(ctx context.Context, id, p string) (types.ContainerPathStat, error) {
	p, err := cleanContainerPath(p)
	if err != nil {
		return types.ContainerPathStat{}, err
	}
	return d.c.ContainerStatPath(ctx, id, p)
}

// ContainerFileDownload opens the Docker archive stream for path inside the
// container. The returned reader is a tar containing the path (a single file's
// content, or a directory tree). The caller must close the reader. stat holds
// metadata for the requested path (use stat.Mode.IsDir() to decide file vs zip).
func (d *Client) ContainerFileDownload(ctx context.Context, id, p string) (io.ReadCloser, types.ContainerPathStat, error) {
	p, err := cleanContainerPath(p)
	if err != nil {
		return nil, types.ContainerPathStat{}, err
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
	return d.c.CopyToContainer(ctx, id, destDir, tarStream, types.CopyToContainerOptions{})
}

// assertContainerPath is a guard used by handlers to refuse obviously bad input
// early; the Docker API itself rejects paths it cannot resolve.
var errBadContainerPath = fmt.Errorf("invalid container path")
