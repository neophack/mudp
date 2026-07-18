package server

import (
	"net/http"
	"strconv"

	"mudp/internal/store"
)

// audit returns recent management actions, optionally filtered by actor/action.
func (a *App) audit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	f := store.AuditFilter{
		Actor:  r.URL.Query().Get("actor"),
		Action: r.URL.Query().Get("action"),
		Target: r.URL.Query().Get("target"),
	}
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		f.Limit = l
	}
	entries, err := a.db.AuditList(f)
	if err == nil {
		// Older rows were written with raw usernames (Feishu accounts logged
		// their open_id). Rewrite any actor/target that exactly matches a known
		// username to that account's display name for readability.
		if users, uerr := a.db.Users(); uerr == nil {
			resolveDisplayNames(entries, users)
		}
	}
	respond(w, entries, err)
}

// resolveDisplayNames rewrites audit actor/target values that exactly match a
// username to the account's display name, when one is set.
func resolveDisplayNames(entries []store.AuditEntry, users []store.User) {
	byName := make(map[string]string, len(users))
	for _, u := range users {
		if u.DisplayName != "" {
			byName[u.Username] = u.DisplayName
		}
	}
	for i := range entries {
		if n, ok := byName[entries[i].Actor]; ok {
			entries[i].Actor = n
		}
		if n, ok := byName[entries[i].Target]; ok {
			entries[i].Target = n
		}
	}
}
