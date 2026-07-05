package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
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
	writeJSON(w, http.StatusOK, []diskInfo{{Name: "root", Path: "/", TotalBytes: 0, FreeBytes: 0, UsedPct: 0}})
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
	if err := decodeJSON(r, &req); err != nil || req.Source == "" || req.Target == "" {
		writeErr(w, http.StatusBadRequest, "source and target are required")
		return
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("New-Item -ItemType Directory -Force -Path %q | Out-Null", req.Target))
	} else {
		args := []string{req.Source, req.Target}
		if req.FSType != "" {
			args = []string{"-t", req.FSType, req.Source, req.Target}
		}
		cmd = exec.Command("mount", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadRequest, strings.TrimSpace(string(out))+" "+err.Error())
		return
	}
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
		cmd = exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("Remove-Item -Force %q", req.Target))
	} else {
		cmd = exec.Command("umount", req.Target)
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
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.TargetDir) == "" {
		writeErr(w, http.StatusBadRequest, "targetDir is required")
		return
	}
	if err := os.MkdirAll(req.TargetDir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := filepath.Join(req.TargetDir, "mudp-backup-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(name)
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
	add(a.cfg.DBPath, filepath.Base(a.cfg.DBPath))
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
