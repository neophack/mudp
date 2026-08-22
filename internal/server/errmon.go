package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"mudp/internal/store"
)

// Error monitoring: every recovered panic and every 5xx response is aggregated
// by fingerprint (kind+method+path+normalized message) into the error_events
// table, the way Sentry groups issues. Recurrences bump a counter instead of
// adding rows, so a hot loop of failures cannot flood the table or the admin
// notifications (which fire once per new fingerprint).

const (
	errorKindPanic = "panic"
	errorKindHTTP  = "http"
)

var digitsRe = regexp.MustCompile(`\d+`)

// errorFingerprint groups same-fault occurrences: variable parts of the
// message (IDs, counts) are collapsed so "user 7 not found" and "user 12 not
// found" land on the same row.
func errorFingerprint(kind, method, path, message string) string {
	msg := digitsRe.ReplaceAllString(message, "#")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + method + "\x00" + path + "\x00" + msg))
	return hex.EncodeToString(sum[:])
}

// recordError stores one aggregated occurrence and reports whether this
// fingerprint was new. Panics notify admins, but only the first time a
// fingerprint appears — the aggregate panel carries the rest.
func (a *App) recordError(kind, method, path, message, stack string) (isNew bool) {
	event := store.ErrorEvent{
		Fingerprint: errorFingerprint(kind, method, path, message),
		Kind:        kind, Method: method, Path: path,
		Message: message, Stack: stack,
	}
	first, err := a.db.RecordErrorEvent(event)
	if err != nil {
		return false
	}
	if first && kind == errorKindPanic {
		a.notifyAdminsSystemAlert(fmt.Sprintf("%s on %s %s: %s", kind, method, path, message))
	}
	return first
}

// errorStatusRecorder observes the status code a handler writes so 5xx
// responses (not just panics) reach the aggregate.
type errorStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *errorStatusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// recordErrors is mounted just inside recoverPanic: panics unwind past it (and
// are recorded by recoverPanic itself), while ordinary 5xx writes land here.
func (a *App) recordErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &errorStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec.status >= http.StatusInternalServerError {
				a.recordError(errorKindHTTP, r.Method, r.URL.Path, fmt.Sprintf("HTTP %d", rec.status), "")
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// errorsList serves the aggregated error events plus summary counts.
func (a *App) errorsList(w http.ResponseWriter, r *http.Request) {
	events, err := a.db.ListErrorEvents()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load error events")
		return
	}
	var panics, occurrences int64
	for _, e := range events {
		if e.Kind == errorKindPanic {
			panics++
		}
		occurrences += e.Count
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"stats":  map[string]int64{"events": int64(len(events)), "panics": panics, "occurrences": occurrences},
	})
}

// errorDelete resolves (removes) one aggregated event.
func (a *App) errorDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.db.DeleteErrorEvent(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to delete event")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// errorsClear empties the aggregate.
func (a *App) errorsClear(w http.ResponseWriter, r *http.Request) {
	if err := a.db.ClearErrorEvents(); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to clear events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// errorsExport streams the aggregate as CSV for offline diagnosis.
func (a *App) errorsExport(w http.ResponseWriter, r *http.Request) {
	events, err := a.db.ListErrorEvents()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load error events")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mudp-errors.csv"`)
	io.WriteString(w, "kind,method,path,message,count,first_seen,last_seen,stack\n")
	for _, e := range events {
		fprintfCSV(w, e.Kind, e.Method, e.Path, e.Message, fmt.Sprint(e.Count), e.FirstSeen, e.LastSeen, e.Stack)
	}
}
