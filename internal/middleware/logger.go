package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"mudp/internal/httpx"
)

// RequestLogger injects a request ID and a structured logger into the request
// context and logs the start/end of every request.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = randomID(16)
		}
		r = httpx.WithRequestID(r, id)
		logger := slog.Default().With("request_id", id, "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		r = httpx.WithLogger(r, logger)
		w.Header().Set("X-Request-ID", id)

		start := time.Now()
		logger.Debug("request started")
		next.ServeHTTP(w, r)
		logger.Info("request completed", "duration_ms", time.Since(start).Milliseconds())
	})
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto rand fails.
		return hex.EncodeToString([]byte(time.Now().String()))[:n*2]
	}
	return hex.EncodeToString(b)
}
