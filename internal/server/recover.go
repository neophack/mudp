package server

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"mudp/internal/httpx"
)

// recoverPanic catches panics in downstream handlers, records a system-alert
// notification for administrators, and returns a generic 500 response.
func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				msg := fmt.Sprint(rec)
				a.notifyAdminsSystemAlert(fmt.Sprintf("Panic on %s %s: %s", r.Method, r.URL.Path, msg))
				httpx.DefaultLogger().Error("panic recovered",
					"error", msg,
					"stack", string(debug.Stack()),
					"path", r.URL.Path,
					"method", r.Method,
				)
				httpx.WriteErr(w, httpx.InternalServerError("internal server error", nil))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
