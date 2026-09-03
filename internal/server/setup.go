package server

import (
	"fmt"
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
	siteName, _ := a.db.Setting("site_name")
	writeJSON(w, http.StatusOK, map[string]any{"setupNeeded": needed, "siteName": siteName})
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
	if err := a.db.CreateUser(req.AdminUsername, req.AdminPassword, store.RoleAdmin, 0, 50, 0); err != nil {
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

// siteSettings lets an admin read/write the site name shown in the browser
// tab and sidebar. Reading is restricted to admins; the value everyone else
// sees is exposed publicly (and pre-login) via setupStatus.
func (a *App) siteSettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		siteName, err := a.db.Setting("site_name")
		respond(w, map[string]string{"siteName": siteName}, err)
	case http.MethodPost:
		var req struct {
			SiteName string `json:"siteName"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(req.SiteName)
		if err := a.db.SaveSetting("site_name", name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.db.Audit(u.Username, "settings.site", "updated site name")
		writeJSON(w, http.StatusOK, map[string]string{"siteName": name})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// userCapacitySettings lets an admin read/write the maximum number of user
// accounts the system will allow. Defaults to store.DefaultUserCapacity.
func (a *App) userCapacitySettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit, err := a.db.UserCapacityLimit()
		respond(w, map[string]int{"capacity": limit}, err)
	case http.MethodPost:
		var req struct {
			Capacity int `json:"capacity"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Capacity < 1 || req.Capacity > 9999 {
			writeErr(w, http.StatusBadRequest, "capacity must be between 1 and 9999")
			return
		}
		if err := a.db.SaveUserCapacityLimit(req.Capacity); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.db.Audit(u.Username, "settings.capacity", fmt.Sprintf("set user capacity to %d", req.Capacity))
		writeJSON(w, http.StatusOK, map[string]int{"capacity": req.Capacity})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// companySettings lets an admin read/write the Feishu tenant key restriction
// applied to Feishu sign-ins. When set, only users whose Feishu tenant key
// matches this value can log in or register. An empty value disables the
// restriction.
func (a *App) companySettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tenantKey, err := a.db.AllowedTenantKey()
		respond(w, map[string]string{"tenantKey": tenantKey}, err)
	case http.MethodPost:
		var req struct {
			TenantKey string `json:"tenantKey"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		tenantKey := strings.TrimSpace(req.TenantKey)
		if err := a.db.SaveAllowedTenantKey(tenantKey); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.db.Audit(u.Username, "settings.company", fmt.Sprintf("set allowed tenant key to %q", tenantKey))
		writeJSON(w, http.StatusOK, map[string]string{"tenantKey": tenantKey})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
