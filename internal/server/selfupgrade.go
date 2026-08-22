package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"mudp/internal/upgrader"
	"mudp/internal/version"
)

// One-click self-upgrade. The old process downloads the release asset for its
// own OS/arch, swaps it in next to the running executable, and restarts:
//
//   - under systemd (INVOCATION_ID/JOURNAL_STREAM set) it exits cleanly and
//     lets systemd restart into the swapped binary;
//   - otherwise it releases the listener, spawns the new binary as a child,
//     and watches it for up to 2 minutes — healthy means exit, dead or stuck
//     means restore the backup, respawn the old binary, and exit.
//
// The new process independently verifies itself at boot (cmd/mudp/main.go):
// healthy-for-a-while commits (backup + marker deleted), never-healthy rolls
// back and exits non-zero. That path covers supervised restarts where no old
// process is left watching.

// upgradeWatchTimeout is how long the old process waits for the replacement
// to become healthy before declaring the upgrade failed and rolling back.
const upgradeWatchTimeout = 2 * time.Minute

func (a *App) SetRestartPrepare(f func()) {
	a.restartPrepare = f
}

// upgradeStatus reports the in-flight (or last known) upgrade phase:
// idle | running:download | running:restarting | error | (gone once the
// process exits — the replacement serves "idle" from its own memory).
func (a *App) upgradeStatus(w http.ResponseWriter, r *http.Request) {
	a.upgradeMu.Lock()
	defer a.upgradeMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"phase":   a.upgradePhase,
		"message": a.upgradeMsg,
		"from":    a.upgradeFrom,
		"to":      a.upgradeTo,
	})
}

// startUpgrade kicks off the one-click upgrade to the given release tag. The
// response returns immediately; poll GET /api/admin/upgrade for progress.
func (a *App) startUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Tag == "" {
		writeErr(w, http.StatusBadRequest, "tag is required")
		return
	}
	if !upgrader.Supported() {
		writeErr(w, http.StatusBadRequest, "no release asset for this platform; upgrade manually")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot resolve running binary")
		return
	}
	if strings.Contains(exe, "go-build") {
		writeErr(w, http.StatusBadRequest, "cannot upgrade a `go run` instance")
		return
	}
	// Fail fast on the classic Linux misconfiguration: root-owned /opt binary
	// directory while the service runs unprivileged (or ProtectSystem=strict).
	// The message carries the directory and user so the admin can fix it in
	// one look.
	if err := upgrader.Precheck(exe); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	a.upgradeMu.Lock()
	if a.upgradePhase == "running:download" || a.upgradePhase == "running:restarting" {
		a.upgradeMu.Unlock()
		writeErr(w, http.StatusConflict, "an upgrade is already in progress")
		return
	}
	a.upgradePhase = "running:download"
	a.upgradeMsg = ""
	a.upgradeFrom = version.Version
	a.upgradeTo = req.Tag
	a.upgradeMu.Unlock()

	a.db.Audit(currentUser(r).Username, "upgrade.start", req.Tag)
	go a.runUpgrade(exe, req.Tag)
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (a *App) runUpgrade(exe, tag string) {
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		log.Printf("upgrade to %s failed: %s", tag, msg)
		a.upgradeMu.Lock()
		a.upgradePhase = "error"
		a.upgradeMsg = msg
		a.upgradeMu.Unlock()
	}

	url, err := upgrader.AssetURL(tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fail("%v", err)
		return
	}
	dest := upgrader.SidecarPath(exe, ".new")
	if err := upgrader.Download(context.Background(), url, dest); err != nil {
		fail("download: %v", err)
		return
	}
	if err := upgrader.WriteMarker(exe, upgrader.Marker{From: version.Version, To: tag}); err != nil {
		fail("marker: %v", err)
		return
	}
	if err := upgrader.Swap(exe, dest); err != nil {
		fail("swap: %v", err)
		return
	}

	a.upgradeMu.Lock()
	a.upgradePhase = "running:restarting"
	a.upgradeMu.Unlock()
	log.Printf("upgrade: staged %s, restarting", tag)

	// Release the listener so the replacement can bind it.
	if a.restartPrepare != nil {
		a.restartPrepare()
	}

	if upgrader.UnderSystemd() {
		// systemd restarts into the swapped binary; the new process's own
		// boot verification handles rollback if it cannot serve.
		log.Printf("upgrade: under systemd, exiting for restart")
		os.Exit(0)
	}

	rolledBack, err := upgrader.RestartAndWatch(exe, os.Args[1:], a.cfg.Addr, upgradeWatchTimeout)
	if err != nil {
		log.Printf("upgrade: %v", err)
		os.Exit(1)
	}
	if rolledBack {
		log.Printf("upgrade: rolled back to %s, previous binary healthy again", a.upgradeFrom)
		os.Exit(1)
	}
	log.Printf("upgrade: %s healthy, old process exiting", tag)
	os.Exit(0)
}
