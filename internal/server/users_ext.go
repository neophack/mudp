package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"mudp/internal/store"
)

// record writes an audit entry for the current request. Best-effort: a store
// error never fails the surrounding handler.
func (a *App) record(r *http.Request, action, target string) {
	actor := "anonymous"
	if u := currentUser(r); u != nil {
		actor = targetName(u)
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

// approveUser moves a pending user into the default users group. It is the
// one-click approval flow used by administrators.
func (a *App) approveUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if err := decodeJSON(r, &req); err != nil || req.UserID == 0 {
		writeErr(w, http.StatusBadRequest, "userId is required")
		return
	}
	u, err := a.db.UserByID(req.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if !isPending(u) {
		writeErr(w, http.StatusBadRequest, "user is not pending approval")
		return
	}
	gid, err := a.db.DefaultUserGroupID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.SetUserGroups(req.UserID, []int64{gid}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Clear the hard-disable flag. Older versions coupled DeactivateUser with
	// disabled=1, so historical rows may carry a stale flag; clearing it on
	// approval guarantees the user can actually use the platform afterwards.
	disabledOff := false
	if err := a.db.UpdateUser(req.UserID, "", "", 0, nil, &disabledOff); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Ensure user netdisk directories and shortcuts/symlinks are created
	if netdiskPath, err := a.db.NetdiskPathForUser(req.UserID); err == nil && netdiskPath != "" {
		_ = EnsureUserNetdiskDir(netdiskPath, u.Username, fmt.Sprintf("%d", u.ID), u.DisplayName)
	}
	a.record(r, "user.approve", targetName(u))
	a.notifyUserApproved(req.UserID, []string{store.DefaultUserGroup})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deactivateUser disables an account and returns it to the pending group.
func (a *App) deactivateUser(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, "you cannot deactivate your own account")
		return
	}
	target, err := a.db.UserByID(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if err := a.db.DeactivateUser(req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "user.deactivate", targetName(target))
	a.notifyUserDeactivated(req.ID)
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

// targetName renders a user for audit logs: the display name when one is set
// (Feishu accounts carry their open_id as username), otherwise the username.
func targetName(u *store.User) string {
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// userDisplayName resolves a username to its audit display name. Unknown or
// deleted users keep their raw username.
func (a *App) userDisplayName(username string) string {
	if username == "" {
		return ""
	}
	if u, err := a.db.UserByUsername(username); err == nil && u != nil {
		return targetName(u)
	}
	return username
}
