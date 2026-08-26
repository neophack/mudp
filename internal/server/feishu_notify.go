package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"mudp/internal/store"
)

const feishuSendTimeout = 5 * time.Second

var errFeishuNotConfigured = errors.New("feishu is not configured")

// sendFeishuText delivers a bot message to one user's Feishu open_id through
// the admin-configured SSO app and records the attempt (sent or failed) in the
// user's send history. Errors are returned so callers (the admin test
// endpoint, the process watcher) can surface them too.
func (a *App) sendFeishuText(userID int64, openID, kind, text string) error {
	err := a.deliverFeishuText(openID, text)
	record := store.FeishuMessage{
		UserID: userID, Kind: kind, OpenID: openID, Message: text,
		Status: store.FeishuMessageSent, CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err != nil {
		record.Status = store.FeishuMessageFailed
		record.Error = err.Error()
	}
	if dbErr := a.db.AddFeishuMessage(record); dbErr != nil {
		// The delivery result matters more than the history row; never mask a
		// delivery failure with a logging one.
		if err == nil {
			err = dbErr
		}
	}
	return err
}

// deliverFeishuText performs the actual bot send without touching history.
func (a *App) deliverFeishuText(openID, text string) error {
	fc, err := a.feishu()
	if err != nil {
		return err
	}
	if fc == nil {
		return errFeishuNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), feishuSendTimeout)
	defer cancel()
	return fc.SendText(ctx, openID, text)
}

// feishuNotifyTest lets an admin push a test message to any user through the
// app bot, verifying the bot capability and messaging scope end to end.
func (a *App) feishuNotifyTest(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "insufficient privileges")
		return
	}
	var req struct {
		UserID  int64  `json:"userId"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil || req.UserID == 0 {
		writeErr(w, http.StatusBadRequest, "userId is required")
		return
	}
	target, err := a.db.UserByID(req.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if target.FeishuOpenID == "" {
		writeErr(w, http.StatusBadRequest, "user has no Feishu account linked")
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "MUDP test message"
	}
	if err := a.sendFeishuText(target.ID, target.FeishuOpenID, store.FeishuKindAdminTest, message); err != nil {
		writeErr(w, http.StatusBadGateway, "Feishu rejected the message: "+err.Error())
		return
	}
	a.record(r, "feishu.test", target.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// feishuMessages returns the caller's own Feishu send history (newest first).
func (a *App) feishuMessages(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	msgs, err := a.db.FeishuMessagesForUser(u.ID, 50)
	respond(w, msgs, err)
}

// feishuMessagesClear deletes the caller's entire send history.
func (a *App) feishuMessagesClear(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	respond(w, map[string]bool{"ok": true}, a.db.ClearFeishuMessages(u.ID))
}
