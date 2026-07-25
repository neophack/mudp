package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
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
		logger := httpx.DefaultLogger().With("request_id", id, "method", r.Method, "path", redactPath(r.URL.Path), "remote", r.RemoteAddr)
		r = httpx.WithLogger(r, logger)
		w.Header().Set("X-Request-ID", id)

		start := time.Now()
		logger.Debug("request started")
		next.ServeHTTP(w, r)
		logger.Info("request completed", "duration_ms", time.Since(start).Milliseconds())
	})
}

// redactPath masks the capability tokens that appear in URL paths before they
// are written to mudp's own logs: /mcp/{token} authenticates a container-scoped
// session, and /pan/{token} grants access to a netdisk share. Either one in a
// log file is equivalent to a password in a log file.
func redactPath(path string) string {
	for _, prefix := range []string{"/mcp/", "/pan/"} {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		if rest == "" {
			return path
		}
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return prefix + "[redacted]" + rest[slash:]
		}
		return prefix + "[redacted]"
	}
	return path
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto rand fails.
		return hex.EncodeToString([]byte(time.Now().String()))[:n*2]
	}
	return hex.EncodeToString(b)
}
