package server

import (
	"net/http"
	"os"
	"strings"

	"mudp/internal/store"
)

// setupStatus reports whether the server still needs initial administrator
// configuration. It is intentionally public so the SPA can route to the setup
// wizard before any account exists.
func (a *App) setupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := a.setupNeeded()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setupNeeded": needed})
}

// setupNeeded returns true when no administrator account exists and the
// setup wizard has not been completed.
func (a *App) setupNeeded() (bool, error) {
	var n int
	if err := a.db.QueryRow(`select count(*) from users where role='admin'`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	completed, err := a.db.Setting("setup_completed")
	if err != nil {
		return false, err
	}
	return completed != "1" && completed != "true", nil
}

// setupInit creates the first administrator account and default groups. It may
// only be called once; afterwards setupNeeded() returns false.
func (a *App) setupInit(w http.ResponseWriter, r *http.Request) {
	// setupInit is intentionally public (no session yet exists), so the
	// check-then-act below (see setupNeeded) must be serialized: without this
	// lock, two concurrent requests can both observe "needed" and each create
	// their own admin account before either commits.
	a.setupMu.Lock()
	defer a.setupMu.Unlock()

	needed, err := a.setupNeeded()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !needed {
		writeErr(w, http.StatusBadRequest, "initial setup has already been completed")
		return
	}

	var req struct {
		AdminUsername         string `json:"adminUsername"`
		AdminPassword         string `json:"adminPassword"`
		SiteName              string `json:"siteName"`
		UsersGroupNetdiskPath string `json:"usersGroupNetdiskPath"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.AdminUsername = strings.TrimSpace(req.AdminUsername)
	if req.AdminUsername == "" || req.AdminPassword == "" {
		writeErr(w, http.StatusBadRequest, "admin username and password are required")
		return
	}

	if err := a.db.EnsureDefaultGroups(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.CreateUser(req.AdminUsername, req.AdminPassword, store.RoleAdmin, nil, 50, 0); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Apply the default users group netdisk path if provided.
	if req.UsersGroupNetdiskPath != "" {
		gid, err := a.db.DefaultUserGroupID()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := os.MkdirAll(req.UsersGroupNetdiskPath, 0750); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.db.UpdateGroupNetdiskPath(gid, req.UsersGroupNetdiskPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if req.SiteName != "" {
		_ = a.db.SaveSetting("site_name", strings.TrimSpace(req.SiteName))
	}
	if err := a.db.SaveSetting("setup_completed", "1"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	a.db.Audit(req.AdminUsername, "setup.complete", "initial setup completed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
