package server

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"mudp/internal/store"
)

// Role rank: higher = more powerful. Used to gate endpoints by minimum role.
// Pending users (Feishu, awaiting approval) are blocked by the activated
// middleware, which sits in front of requireRole for every business endpoint.
const (
	rankReadonly = 10
	rankHelpdesk = 20
	rankUser     = 30
	rankOperator = 40
	rankAdmin    = 50
)

func roleRank(r string) int {
	switch r {
	case store.RoleReadonly:
		return rankReadonly
	case store.RoleHelpdesk:
		return rankHelpdesk
	case store.RoleUser:
		return rankUser
	case store.RoleOperator:
		return rankOperator
	case store.RoleAdmin:
		return rankAdmin
	}
	return 0
}

// requireRole returns an http.Handler that runs auth → activated → min-role
// checks around next. It is used for the few standalone routes mounted outside
// a chi.Group (e.g. /api/dashboard). Inside groups the same three checks are
// applied as middleware for efficiency.
func (a *App) requireRole(minRank int, next http.HandlerFunc) http.Handler {
	chain := a.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPending(currentUser(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "account pending approval", "pending": true})
			return
		}
		if roleRank(currentUser(r).Role) < minRank {
			writeErr(w, http.StatusForbidden, "insufficient privileges")
			return
		}
		next.ServeHTTP(w, r)
	}))
	return chain
}

// admin wraps a handler with the admin-only gate. Convenience for ad-hoc admin
// endpoints; equivalent to requireRole(rankAdmin, ...).
func (a *App) admin(next http.HandlerFunc) http.Handler {
	return a.requireRole(rankAdmin, next)
}

// canMutate reports whether the current user may perform write operations on
// managed Docker resources. Readonly and helpdesk are read-only tiers; any
// unrecognised role is treated as read-only too (fail-closed).
func canMutate(u *store.User) bool {
	if u == nil {
		return false
	}
	switch u.Role {
	case store.RoleAdmin, store.RoleOperator, store.RoleUser:
		return true
	}
	return false
}

// recoverPanic turns a panicking handler into a 500 so the server keeps serving
// and the error is logged instead of crashing the goroutine.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("panic: %v\n%s\n", rec, debug.Stack())
				writeErr(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
