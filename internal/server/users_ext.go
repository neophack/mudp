package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"mudp/internal/store"
)

// record writes an audit entry for the current request. Best-effort: a store
// error never fails the surrounding handler.
func (a *App) record(r *http.Request, action, target string) {
	actor := "anonymous"
	if u := currentUser(r); u != nil {
		actor = u.Username
	}
	a.db.Audit(actor, action, target)
}

// userUpdate mutates a user's password, role, container cap, or disabled flag.
// Admins cannot revoke their own admin role or disable themselves — that guard
// prevents a single keystroke from locking everyone out.
func (a *App) userUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ID                int64  `json:"id"`
		Password          string `json:"password"`
		Role              string `json:"role"`
		ContainerCap      int    `json:"containerCap"`
		NetdiskQuotaBytes *int64 `json:"netdiskQuotaBytes"`
		PortPrefix        *int   `json:"portPrefix"`
		Disabled          *bool  `json:"disabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Role != "" && !store.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	// Self-protection: an admin may not demote or disable themselves.
	caller := currentUser(r)
	if caller.ID == req.ID && caller.Role == store.RoleAdmin {
		if req.Role != "" && req.Role != store.RoleAdmin {
			writeErr(w, http.StatusBadRequest, "you cannot remove your own admin role")
			return
		}
		if req.Disabled != nil && *req.Disabled {
			writeErr(w, http.StatusBadRequest, "you cannot disable your own account")
			return
		}
	}
	target, _ := a.db.UserByID(req.ID)
	if req.PortPrefix != nil && (*req.PortPrefix < 0 || *req.PortPrefix > 655) {
		writeErr(w, http.StatusBadRequest, "port prefix must be between 0 and 655")
		return
	}
	if err := a.db.UpdateUser(req.ID, req.Password, req.Role, req.ContainerCap, req.NetdiskQuotaBytes, req.Disabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.PortPrefix != nil {
		if err := a.db.UpdateUserPortPrefix(req.ID, *req.PortPrefix); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	name := targetName(target)
	a.record(r, "user.update", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// userDelete removes a user. Admins cannot delete themselves.
func (a *App) userDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	caller := currentUser(r)
	if caller.ID == req.ID {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	target, _ := a.db.UserByID(req.ID)
	if err := a.db.DeleteUser(req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "user.delete", targetName(target))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeJSON is the single decoder used by POST handlers: it rejects malformed
// JSON and bodies that exceed a sane size.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20)) // 4 MiB cap
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid json")
	}
	if dec.More() {
		return errors.New("invalid json")
	}
	return nil
}

func targetName(u *store.User) string {
	if u == nil {
		return ""
	}
	return u.Username
}
