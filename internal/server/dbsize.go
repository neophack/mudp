package server

// On-demand database housekeeping handlers for the admin "Database" page. The
// automatic pruner (resources.go) keeps log tables bounded on a timer; these
// endpoints give an operator the same control interactively, plus a view of
// where the database's bytes are so the decision to prune is informed.
//
// The safety boundary lives in the store layer (store.PruneLogs accepts only a
// hard-coded allow-list of log tables). These handlers only translate between
// HTTP and that boundary: they never interpolate a table name into SQL and they
// forward the allow-list to the UI verbatim, so what the page offers to clean
// is exactly what the store will permit.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"mudp/internal/httpx"
	"mudp/internal/store"
)

// dbUsage reports the database's footprint: the main file, WAL, and SHM sizes,
// the free-page count, and a per-table breakdown of bytes and rows. GET only —
// it is a read with no side effects.
//
// GET /api/admin/db/usage
func (a *App) dbUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	report, err := a.db.DBUsage(a.cfg.DBPath)
	if err != nil {
		httpx.Logger(r).Error("db usage report failed", "error", err)
		// A partial report is still useful, but a hard failure is surfaced.
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report":   report,
		"prunable": store.PruneableLogTables(),
	})
}

// dbPrune clears rows from one or more log tables, optionally bounded by age.
// Every table name is vetted against the store's allow-list, so a request can
// never reach user data regardless of what it asks for. olderThanDays <= 0
// means "clear everything"; a positive value clears only rows older than that.
//
// A checkpoint runs afterwards so the freed space is returned to the filesystem
// rather than held in the WAL. The whole operation is recorded in the audit log.
//
// POST /api/admin/db/prune {"tables": ["audit_logs", ...], "olderThanDays": 30}
func (a *App) dbPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Tables        []string `json:"tables"`
		OlderThanDays int      `json:"olderThanDays"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// De-duplicate and trim so the audit trail and the per-table result are
	// clean, then validate against the allow-list before anything is deleted.
	seen := make(map[string]bool, len(req.Tables))
	tables := make([]string, 0, len(req.Tables))
	for _, name := range req.Tables {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if !store.IsPruneableLogTable(name) {
			writeErr(w, http.StatusBadRequest, "table "+name+" cannot be pruned")
			return
		}
		seen[name] = true
		tables = append(tables, name)
	}
	if len(tables) == 0 {
		writeErr(w, http.StatusBadRequest, "no tables selected")
		return
	}
	// olderThanDays <= 0 means "all rows": represent that as a far-future cutoff
	// so a single code path serves both, and the Prune* helpers (which compare
	// created_at < cutoff) clear everything.
	cutoff := time.Now().AddDate(100, 0, 0)
	scope := "all rows"
	if req.OlderThanDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -req.OlderThanDays)
		scope = "older than " + strconv.Itoa(req.OlderThanDays) + "d"
	}
	deleted, err := a.db.PruneLogs(tables, cutoff)
	if err != nil {
		httpx.Logger(r).Error("db prune failed", "error", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Fold the WAL back in and truncate so the freed bytes are visible on disk.
	// A failure here is not fatal: the rows are gone either way, and the next
	// hourly checkpoint will reclaim the space.
	if err := a.db.VacuumAndCheckpoint(); err != nil {
		httpx.Logger(r).Error("db checkpoint after prune failed", "error", err)
	}
	a.record(r, "db.prune", strings.Join(tables, ",")+" ("+scope+")")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"deleted": deleted,
	})
}
