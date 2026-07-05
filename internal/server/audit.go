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
	respond(w, entries, err)
}
