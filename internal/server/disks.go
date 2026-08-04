package server

import (
	"archive/zip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type diskInfo struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	TotalBytes uint64  `json:"totalBytes"`
	FreeBytes  uint64  `json:"freeBytes"`
	UsedPct    float64 `json:"usedPct"`
}

type diskMountConfig struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	FSType       string `json:"fsType"`
	BackupTarget string `json:"backupTargetDir"`
}

func (a *App) diskMountConfigGet(w http.ResponseWriter, r *http.Request) {
	source, _ := a.db.Setting("disk_mount_source")
	target, _ := a.db.Setting("disk_mount_target")
	fsType, _ := a.db.Setting("disk_mount_fstype")
	backupTarget, _ := a.db.Setting("disk_backup_target_dir")
	writeJSON(w, http.StatusOK, diskMountConfig{
		Source:       strings.TrimSpace(source),
		Target:       strings.TrimSpace(target),
		FSType:       strings.TrimSpace(fsType),
		BackupTarget: strings.TrimSpace(backupTarget),
	})
}

func (a *App) diskMountConfigPost(w http.ResponseWriter, r *http.Request) {
	var req diskMountConfig
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Target = strings.TrimSpace(req.Target)
	req.FSType = strings.TrimSpace(req.FSType)
	req.BackupTarget = strings.TrimSpace(req.BackupTarget)
	if err := a.db.SaveSetting("disk_mount_source", req.Source); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.SaveSetting("disk_mount_target", req.Target); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.SaveSetting("disk_mount_fstype", req.FSType); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.SaveSetting("disk_backup_target_dir", req.BackupTarget); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "disk.config.save", req.Target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) disks(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell", "-NoProfile", "-Command", `Get-CimInstance Win32_LogicalDisk | Select-Object DeviceID,Size,FreeSpace,VolumeName | ConvertTo-Json`).Output()
		if err == nil {
			var raw []struct {
				DeviceID   string
				Size       uint64
				FreeSpace  uint64
				VolumeName string
			}
			if out = bytesTrimBOM(out); len(out) > 0 && out[0] == '{' {
				var one struct {
					DeviceID   string
					Size       uint64
					FreeSpace  uint64
					VolumeName string
				}
				_ = json.Unmarshal(out, &one)
				raw = append(raw, one)
			} else {
				_ = json.Unmarshal(out, &raw)
			}
			var disks []diskInfo
			for _, d := range raw {
				used := float64(0)
				if d.Size > 0 {
					used = float64(d.Size-d.FreeSpace) / float64(d.Size) * 100
				}
				disks = append(disks, diskInfo{Name: d.VolumeName, Path: d.DeviceID + `\`, TotalBytes: d.Size, FreeBytes: d.FreeSpace, UsedPct: used})
			}
			writeJSON(w, http.StatusOK, disks)
			return
		}
	}
	writeJSON(w, http.StatusOK, hostDisks())
}

// hostDisks enumerates disks on non-Windows hosts. Linux mounts are read from
// /proc/mounts, keeping only entries backed by a real block device (loop
// devices excluded) so pseudo filesystems (proc, tmpfs, overlay, ...) stay
// hidden; several mounts of one device collapse into its first entry. When
// /proc/mounts is unreadable or yields nothing (e.g. macOS) it falls back to
// reporting the root filesystem alone.
func hostDisks() []diskInfo {
	seen := map[string]bool{}
	var out []diskInfo
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			source, target := fields[0], fields[1]
			if !strings.HasPrefix(source, "/dev/") || strings.HasPrefix(source, "/dev/loop") || seen[source] {
				continue
			}
			seen[source] = true
			if d, ok := diskStat(source, target); ok {
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		if d, ok := diskStat("root", "/"); ok {
			out = append(out, d)
		}
	}
	return out
}

func diskStat(name, path string) (diskInfo, bool) {
	total, free, err := diskUsage(path)
	if err != nil || total == 0 {
		return diskInfo{}, false
	}
	return diskInfo{
		Name:       name,
		Path:       path,
		TotalBytes: total,
		FreeBytes:  free,
		UsedPct:    float64(total-free) / float64(total) * 100,
	}, true
}

func bytesTrimBOM(b []byte) []byte {
	return []byte(strings.TrimPrefix(string(b), "\ufeff"))
}

func (a *App) diskMount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Target string `json:"target"`
		FSType string `json:"fsType"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Target = strings.TrimSpace(req.Target)
	req.FSType = strings.TrimSpace(req.FSType)
	if req.Source == "" {
		req.Source, _ = a.db.Setting("disk_mount_source")
	}
	if req.Target == "" {
		req.Target, _ = a.db.Setting("disk_mount_target")
	}
	if req.FSType == "" {
		req.FSType, _ = a.db.Setting("disk_mount_fstype")
	}
	if strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Target) == "" {
		writeErr(w, http.StatusBadRequest, "source and target are required")
		return
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "New-Item -ItemType Directory -Force -Path "+escapePathForPowerShell(req.Target)+" | Out-Null") // #nosec G204 -- req.Target is single-quote-escaped by escapePathForPowerShell (no PS interpolation); admin-only endpoint
	} else {
		args := []string{req.Source, req.Target}
		if req.FSType != "" {
			args = []string{"-t", req.FSType, req.Source, req.Target}
		}
		cmd = exec.Command("mount", args...) // #nosec G204 -- separate argv, no shell; mount takes a literal mountpoint
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadRequest, strings.TrimSpace(string(out))+" "+err.Error())
		return
	}
	_ = a.db.SaveSetting("disk_mount_source", req.Source)
	_ = a.db.SaveSetting("disk_mount_target", req.Target)
	_ = a.db.SaveSetting("disk_mount_fstype", req.FSType)
	a.record(r, "disk.mount", req.Target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) diskUnmount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Target == "" {
		writeErr(w, http.StatusBadRequest, "target is required")
		return
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// Single-quote the path: %q produces a PowerShell *double*-quoted string,
		// which still interpolates $(...) and backticks, so a target like
		// "$(calc)" would execute. escapePathForPowerShell emits a single-quoted
		// literal instead, where no interpolation happens.
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Remove-Item -Force "+escapePathForPowerShell(req.Target)) // #nosec G204 -- single-quote-escaped target; admin-only endpoint
	} else {
		cmd = exec.Command("umount", req.Target) // #nosec G204 -- separate argv, no shell
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadRequest, strings.TrimSpace(string(out))+" "+err.Error())
		return
	}
	a.record(r, "disk.unmount", req.Target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) backupData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetDir string `json:"targetDir"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.TargetDir = strings.TrimSpace(req.TargetDir)
	if req.TargetDir == "" {
		req.TargetDir, _ = a.db.Setting("disk_backup_target_dir")
	}
	if strings.TrimSpace(req.TargetDir) == "" {
		writeErr(w, http.StatusBadRequest, "targetDir is required (set backup directory in Disks page)")
		return
	}
	if err := os.MkdirAll(req.TargetDir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := filepath.Join(req.TargetDir, "mudp-backup-"+time.Now().Format("20060102-150405")+".zip")
	// Copying the live DB file directly is unsafe under WAL: recently committed
	// data can still be sitting in the "-wal" file and never gets copied, so the
	// backup can be torn/incomplete. VACUUM INTO asks SQLite itself to write out
	// a fully consistent, checkpointed snapshot, which we then zip instead of
	// the live file.
	snapshotPath := name + ".snapshot.tmp"
	_ = os.Remove(snapshotPath) // VACUUM INTO refuses to overwrite an existing file
	if _, err := a.db.ExecContext(r.Context(), "VACUUM INTO ?", snapshotPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "backup snapshot failed: "+err.Error())
		return
	}
	defer os.Remove(snapshotPath)
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	add := func(path, arc string) {
		in, err := os.Open(path)
		if err != nil {
			return
		}
		defer in.Close()
		wr, err := zw.Create(arc)
		if err != nil {
			return
		}
		_, _ = io.Copy(wr, in)
	}
	add(snapshotPath, filepath.Base(a.cfg.DBPath))
	_ = a.db.SaveSetting("disk_backup_target_dir", req.TargetDir)
	a.record(r, "backup.create", name)
	writeJSON(w, http.StatusOK, map[string]string{"path": name})
}

func (a *App) groupNetdisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID int64  `json:"groupId"`
		Path    string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || req.GroupID == 0 {
		writeErr(w, http.StatusBadRequest, "groupId is required")
		return
	}
	if req.Path != "" {
		if err := os.MkdirAll(req.Path, 0750); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	respond(w, map[string]bool{"ok": true}, a.db.UpdateGroupNetdiskPath(req.GroupID, req.Path))
}

// groupBackup sets the per-group backup disk root, mirroring groupNetdisk. We do
// NOT create the directory here: the backup disk may be a slow mechanical drive
// and we only want to touch it once a backup actually runs.
func (a *App) groupBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID int64  `json:"groupId"`
		Path    string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || req.GroupID == 0 {
		writeErr(w, http.StatusBadRequest, "groupId is required")
		return
	}
	respond(w, map[string]bool{"ok": true}, a.db.UpdateGroupBackupPath(req.GroupID, req.Path))
}

// groupSharedDisk sets the per-group shared-disk root, mirroring groupNetdisk.
func (a *App) groupSharedDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID int64  `json:"groupId"`
		Path    string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || req.GroupID == 0 {
		writeErr(w, http.StatusBadRequest, "groupId is required")
		return
	}
	if req.Path != "" {
		if err := os.MkdirAll(req.Path, 0750); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	respond(w, map[string]bool{"ok": true}, a.db.UpdateGroupSharedDiskPath(req.GroupID, req.Path))
}
