package server

import (
	"fmt"
	"net/http"
	"strings"

	"mudp/internal/store"
)

// notifications returns the current user's notifications plus an unread count.
func (a *App) notifications(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	items, unread, err := a.db.NotificationsForUser(u.ID, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "unreadCount": unread})
}

// notificationsRead marks one or more notifications as read.
func (a *App) notificationsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if req.All {
		err = a.db.MarkAllNotificationsRead(u.ID)
	} else {
		err = a.db.MarkNotificationsRead(u.ID, req.IDs)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// notificationsDelete removes one or more notifications for the current user.
func (a *App) notificationsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if req.All {
		err = a.db.DeleteAllNotifications(u.ID)
	} else {
		err = a.db.DeleteNotifications(u.ID, req.IDs)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// notifyAdminsPendingUser creates a notification for every admin when a new
// user lands in the pending group.
func (a *App) notifyAdminsPendingUser(u *store.User) {
	display := displayName(u)
	_ = a.db.NotifyAdmins(store.Notification{
		Type:    store.NotificationPendingUser,
		Title:   "New user awaiting approval",
		Message: fmt.Sprintf("%s joined the pending group and needs to be assigned to a business group.", display),
		Data: map[string]any{
			"userId":   u.ID,
			"username": u.Username,
			"name":     display,
		},
	})
}

// notifyUserApproved creates a notification for a user who was just approved.
func (a *App) notifyUserApproved(userID int64, group string) {
	if group == "" {
		group = store.DefaultUserGroup
	}
	_ = a.db.NotifyUser(userID, store.Notification{
		Type:    store.NotificationUserApproved,
		Title:   "Account approved",
		Message: fmt.Sprintf("Your account has been approved and added to the %s group.", group),
		Data:    map[string]any{"group": group},
	})
}

// notifyUserDeactivated creates a notification for a user whose account was
// deactivated and moved back to the pending group.
func (a *App) notifyUserDeactivated(userID int64) {
	_ = a.db.NotifyUser(userID, store.Notification{
		Type:    store.NotificationUserDeactivated,
		Title:   "Account deactivated",
		Message: "Your account has been deactivated and returned to the pending approval group. Contact an administrator for assistance.",
		Data:    map[string]any{},
	})
}

// notifyAdminsSystemAlert creates a notification for every admin when a panic
// or other serious server error is recovered.
func (a *App) notifyAdminsSystemAlert(msg string) {
	_ = a.db.NotifyAdmins(store.Notification{
		Type:    store.NotificationSystemAlert,
		Title:   "System exception",
		Message: msg,
		Data:    map[string]any{},
	})
}

// displayName returns a human-readable name for a user.
func displayName(u *store.User) string {
	if u == nil {
		return "Unknown"
	}
	if strings.TrimSpace(u.DisplayName) != "" {
		return strings.TrimSpace(u.DisplayName)
	}
	return u.Username
}
