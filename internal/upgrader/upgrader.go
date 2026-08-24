// Package upgrader implements mudp's one-click self-upgrade: resolve the
// release asset for the running OS/arch, download it, swap it in next to the
// running binary, and provide the commit/rollback primitives both processes
// cooperate on:
//
//   - old process: download -> Swap (exe→bak, new→exe) -> restart
//   - new process: boot; healthy for a while -> Commit (drop bak + marker);
//     cannot serve -> Rollback (exe→failed, bak→exe) and exit
//
// The marker file (mudp-upgrade.json next to the executable) is what tells the
// new process it is an upgrade attempt rather than an ordinary start.
package upgrader

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
)

const releaseAssetBaseURL = "https://github.com/neophack/mudp/releases/download"

// maxAssetBytes caps the downloaded binary so a misbehaving mirror cannot fill
// the disk. Release binaries are ~20-25 MB; 512 MB is a generous ceiling.
const maxAssetBytes = 512 << 20

// downloadTimeout bounds the whole asset download.
const downloadTimeout = 10 * time.Minute

// AssetName maps the running platform to its release binary file name,
// openp2p-style `<name>-<os>-<arch>` (mudp-windows-amd64.exe). Empty means
// the platform has no published binary (auto-upgrade unsupported).
func AssetName(goos, goarch string) string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	switch {
	case goos == "windows" && goarch == "amd64":
		return "mudp-windows-amd64" + ext
	case goos == "windows" && goarch == "arm64":
		return "mudp-windows-arm64" + ext
	case goos == "linux" && goarch == "amd64":
		return "mudp-linux-amd64" + ext
	case goos == "linux" && goarch == "arm64":
		return "mudp-linux-arm64" + ext
	}
	return ""
}

// ArchiveName returns the release asset file name for a tag: Windows binaries
// ship as zip, everything else as tar.gz, both carrying the version —
// mudp-windows-amd64-v1.2.0.zip, mudp-linux-arm64-v1.2.0.tar.gz.
func ArchiveName(tag, goos, goarch string) string {
	name := AssetName(goos, goarch)
	if strings.HasSuffix(name, ".exe") {
		return strings.TrimSuffix(name, ".exe") + "-" + tag + ".zip"
	}
	return name + "-" + tag + ".tar.gz"
}

// AssetURL returns the download URL of a tag's release archive for the
// running platform.
func AssetURL(tag, goos, goarch string) (string, error) {
	name := ArchiveName(tag, goos, goarch)
	if AssetName(goos, goarch) == "" {
		return "", fmt.Errorf("no release asset for %s/%s", goos, goarch)
	}
	return fmt.Sprintf("%s/%s/%s", releaseAssetBaseURL, tag, name), nil
}

// Supported reports whether this platform can auto-upgrade at all.
func Supported() bool {
	return AssetName(runtime.GOOS, runtime.GOARCH) != ""
}

// UnderSupervisor reports whether the process is managed by a service
// supervisor: systemd (which sets INVOCATION_ID / JOURNAL_STREAM) or the
// Windows service controller. Under a supervisor the old process must NOT
// spawn the new binary itself — a spawned child shares the dying process's
// console/session and goes down with it. Exiting instead lets the supervisor
// restart into the already-swapped executable, and the new process's own
// failure path performs the rollback.
func UnderSupervisor() bool {
	return UnderSystemd() || (runtime.GOOS == "windows" && !service.Interactive())
}

// UnderSystemd reports whether the process was started by systemd.
func UnderSystemd() bool {
	return os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != ""
}

// Download fetches url into dest. onProgress (optional) receives the running
// byte count and the total from Content-Length (0 when unknown) as the body
// streams in. The result is guaranteed executable: the create mode 0o755 can
// be weakened by a restrictive umask, and a leftover dest file keeps its old
// mode on O_TRUNC, so the exec bit is set explicitly afterwards (a no-op on
// Windows where Chmod only toggles the read-only bit).
func Download(ctx context.Context, url, dest string, onProgress func(read, total int64)) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	// A partial download must never be mistaken for a usable binary: write to
	// the side file and let Swap move it into place only on success.
	total := resp.ContentLength
	var read int64
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(dest)
				return werr
			}
			read += int64(n)
			if read > maxAssetBytes {
				f.Close()
				os.Remove(dest)
				return fmt.Errorf("download exceeds %d bytes", maxAssetBytes)
			}
			if onProgress != nil {
				onProgress(read, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			os.Remove(dest)
			return err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	if fi, err := os.Stat(dest); err == nil && fi.Size() == 0 {
		os.Remove(dest)
		return fmt.Errorf("downloaded asset is empty")
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		os.Remove(dest)
		return fmt.Errorf("make downloaded binary executable: %w", err)
	}
	return nil
}

// ExtractBinary pulls the platform binary out of a release archive (zip on
// Windows, tar.gz elsewhere) into dest, ready to be swapped in. The archive
// is expected to contain the binary under its AssetName; a single-member
// archive under any name is accepted as a fallback.
func ExtractBinary(archive, dest, goos, goarch string) error {
	want := AssetName(goos, goarch)
	var err error
	if strings.HasSuffix(archive, ".zip") {
		err = extractFromZip(archive, want, dest)
	} else {
		err = extractFromTarGz(archive, want, dest)
	}
	if err != nil {
		return err
	}
	if fi, statErr := os.Stat(dest); statErr != nil || fi.Size() == 0 {
		return fmt.Errorf("archive contained no usable binary")
	}
	return os.Chmod(dest, 0o755)
}

func extractFromZip(archive, want, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()
	var fallback *zip.File
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if filepath.Base(f.Name) == want {
			return extractTo(f.Open, dest)
		}
		if fallback == nil {
			fallback = f
		}
	}
	if fallback == nil {
		return fmt.Errorf("zip contains no file")
	}
	return extractTo(fallback.Open, dest)
}

func extractFromTarGz(archive, want, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	fallback := false
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) == want {
			return extractTo(func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }, dest)
		}
		fallback = true
	}
	if !fallback {
		return fmt.Errorf("tar contains no file")
	}
	// Rewind and take the first regular member.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := gz.Reset(f); err != nil {
		return err
	}
	tr = tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("tar contains no file")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			return extractTo(func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }, dest)
		}
	}
}

// extractTo streams an opened archive member into dest.
func extractTo(open func() (io.ReadCloser, error), dest string) error {
	src, err := open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("extract: %w", err)
	}
	return out.Close()
}

// Precheck verifies the upgrade can actually write the files it must replace
// before anything is downloaded: the executable's directory needs to be
// writable by the current user (Linux deployments often keep the binary in a
// root-owned /opt while the service runs as an unprivileged user, or sandbox
// it with systemd ProtectSystem=strict). Failing here — with the directory
// and user in the message — is far kinder than failing mid-download.
func Precheck(exe string) error {
	dir := filepath.Dir(exe)
	probe := filepath.Join(dir, ".mudp-upgrade-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return fmt.Errorf("binary directory %s is not writable by user %s — chown it to the service user (or add it to ReadWritePaths under systemd) and retry: %w",
			dir, currentUser(), err)
	}
	os.Remove(probe)
	return nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// SidecarPath returns exe with suffix appended before any .exe extension
// (mudp.exe + ".new" → mudp.new.exe), so Windows keeps the executable
// extension on every side file.
func SidecarPath(exe, suffix string) string {
	if filepath.Ext(exe) == ".exe" {
		base := exe[:len(exe)-len(".exe")]
		return base + suffix + ".exe"
	}
	return exe + suffix
}

// MarkerPath returns the fixed marker file path for the directory exe lives in.
func MarkerPath(exe string) string {
	return filepath.Join(filepath.Dir(exe), "mudp-upgrade.json")
}

// Marker records one in-flight upgrade attempt.
type Marker struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   string `json:"at"`
}

// ReadMarker returns the pending-upgrade marker, or nil when the last boot is
// not an upgrade attempt.
func ReadMarker(exe string) *Marker {
	data, err := os.ReadFile(MarkerPath(exe))
	if err != nil {
		return nil
	}
	var m Marker
	if json.Unmarshal(data, &m) != nil || m.To == "" {
		return nil
	}
	return &m
}

// WriteMarker records the attempt the new process will find at boot.
func WriteMarker(exe string, m Marker) error {
	if m.At == "" {
		m.At = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(MarkerPath(exe), data, 0o644)
}

// Swap atomically stages the upgrade: the running executable becomes the .bak
// backup and the downloaded .new file becomes the executable. Both platforms
// allow renaming a running executable, so this works while the old process is
// still alive.
func Swap(exe, newPath string) error {
	bak := SidecarPath(exe, ".bak")
	os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		// Put the old binary back; better a no-op upgrade than a missing one.
		os.Rename(bak, exe)
		return fmt.Errorf("stage new binary: %w", err)
	}
	return nil
}

// Rollback reverses Swap: the failed executable is parked as .failed and the
// .bak backup is restored. Called by the new process when it cannot serve, or
// by the old watcher process when the new one dies before becoming healthy.
func Rollback(exe string) error {
	failed := SidecarPath(exe, ".failed")
	bak := SidecarPath(exe, ".bak")
	if _, err := os.Stat(bak); err != nil {
		return fmt.Errorf("no backup to roll back to")
	}
	os.Remove(failed)
	// The new binary may be the calling process itself; renaming a running
	// executable is allowed on both platforms, deleting is not (on Windows),
	// so park it rather than remove.
	if err := os.Rename(exe, failed); err != nil {
		return fmt.Errorf("park failed binary: %w", err)
	}
	if err := os.Rename(bak, exe); err != nil {
		os.Rename(failed, exe)
		return fmt.Errorf("restore backup: %w", err)
	}
	os.Remove(MarkerPath(exe))
	return nil
}

// Commit finalizes a verified upgrade: drop the marker and the backup.
func Commit(exe string) error {
	if err := os.Remove(MarkerPath(exe)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(SidecarPath(exe, ".bak")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WaitHealthy polls an instance's /healthz until it answers 200 or the
// deadline passes. Used both by the old process (watching its replacement)
// and by the new process (verifying its own boot).
func WaitHealthy(addr string, deadline time.Duration) error {
	healthURL := "http://" + LoopbackAddr(addr) + "/healthz"
	timeout := time.After(deadline)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-timeout:
			return fmt.Errorf("health check did not pass within %s", deadline)
		case <-time.After(500 * time.Millisecond):
			resp, err := client.Get(healthURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

// LoopbackAddr rewrites a listen address into one dialable from this host:
// wildcard binds become 127.0.0.1 so the self health check works no matter
// what the operator bound to.
func LoopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Spawn launches the (already swapped-in) executable as an independent
// process with the given arguments, inheriting the console.
func Spawn(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Start()
}

// RestartAndWatch is the non-systemd restart path: spawn the replacement and
// babysit it. Healthy within the timeout means the upgrade took (rolledBack
// false). Dead or never-healthy means restore the backup and respawn it —
// rolledBack true, err nil when the old version comes back healthy, or the
// error when even that fails. The caller is expected to exit the current
// process afterwards either way.
func RestartAndWatch(exe string, args []string, addr string, timeout time.Duration) (rolledBack bool, err error) {
	if err := Spawn(exe, args); err != nil {
		// Nothing started: roll back so the next start (manual or supervised)
		// uses the old binary again.
		if rbErr := Rollback(exe); rbErr != nil {
			return false, fmt.Errorf("start replacement: %v; rollback: %v", err, rbErr)
		}
		return true, err
	}
	if err := WaitHealthy(addr, timeout); err == nil {
		return false, nil
	}
	// The replacement died or never served: restore the old binary and bring
	// it back up. (The replacement may have rolled the files back itself
	// before dying — Rollback then just reports "no backup", which is fine:
	// the executable on disk is already the old version.)
	if rbErr := Rollback(exe); rbErr != nil {
		return false, fmt.Errorf("replacement unhealthy: %v; rollback: %v", err, rbErr)
	}
	if err := Spawn(exe, args); err != nil {
		return true, fmt.Errorf("restart previous binary: %v", err)
	}
	if err := WaitHealthy(addr, timeout); err != nil {
		return true, fmt.Errorf("previous binary restarted but still unhealthy: %v", err)
	}
	return true, nil
}
