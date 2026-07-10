package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"mudp/internal/httpx"
)

// RecoverPanic catches panics in downstream handlers and returns a 500.
func RecoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Default().Error("panic recovered",
					"error", fmt.Sprint(rec),
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
