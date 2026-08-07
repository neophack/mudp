package middleware

import (
	"net/http"
	"strings"
	"sync"
)

// cspConnectExtra holds the operator-configured connect-src origin(s) appended
// to the baseline policy, so the browser's IP probe may call a self-hosted
// Cloudflare Worker without being blocked by CSP. It is updated by
// SetIPWorkerOrigin when the admin saves the worker URL. Only the bare origin
// ("https://host[:port]", no path/query/fragment) is ever stored here —
// sanitisedIPWorkerOrigin rejects anything else — so a malicious or malformed
// value can neither inject a CSP directive nor broaden the policy beyond one
// host. Read on every response under a RWMutex since settings can change at
// runtime.
var (
	cspMu           sync.RWMutex
	cspConnectExtra = ""
)

// SetIPWorkerOrigin records the worker origin to allow in connect-src. Pass an
// empty string to clear it (worker disabled / removed). Unsafe origins are
// silently dropped: the CSP then simply omits the worker host and the browser
// probe falls back to WebRTC, which is the safe failure mode.
func SetIPWorkerOrigin(origin string) {
	origin = sanitisedIPWorkerOrigin(origin)
	cspMu.Lock()
	cspConnectExtra = origin
	cspMu.Unlock()
}

// sanitisedIPWorkerOrigin returns the origin string only if it is an https URL
// with a host and no path/query/fragment, otherwise "". Keeping the validation
// strict is what makes dynamically extending the CSP header safe: the value is
// concatenated directly into the header, so any space or semicolon in it would
// let an attacker inject a directive. Rejecting on any deviation avoids that.
func sanitisedIPWorkerOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if !strings.HasPrefix(origin, "https://") {
		return ""
	}
	rest := origin[len("https://"):]
	if rest == "" || strings.ContainsAny(rest, " \t\r\n\"';,<>") {
		return ""
	}
	// No path, query, or fragment — only an optional host[:port].
	for _, ch := range rest {
		if ch == '/' || ch == '?' || ch == '#' {
			return ""
		}
	}
	return origin
}

// buildCSP returns the policy string, appending the current worker origin to
// connect-src when one is configured.
func buildCSP() string {
	cspMu.RLock()
	extra := cspConnectExtra
	cspMu.RUnlock()
	connectSrc := "connect-src 'self' ws: wss: https://api.ipify.org https://api64.ipify.org https://ifconfig.me"
	if extra != "" {
		connectSrc += " " + extra
	}
	return "default-src 'self'; " +
		// 'wasm-unsafe-eval' allows WebAssembly.instantiate (the raster YUV/RAW
		// decoder — see cmd/rasterwasm) without granting plain 'unsafe-eval'
		// (arbitrary eval()/Function() of strings stays blocked).
		"script-src 'self' 'wasm-unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"media-src 'self' blob:; " +
		"font-src 'self' data:; " +
		connectSrc + "; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
}

// SecurityHeaders applies the baseline browser protections to every response.
//
// Handlers that serve user-supplied file bodies (netdisk download/preview) set
// their own stricter Content-Type/CSP/nosniff combination; this middleware does
// not overwrite a policy a handler has already chosen.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Deny device APIs the console never uses, so an injected script cannot
		// reach them either.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if isSecure(r) {
			// Only meaningful over TLS, and harmful to send on plain HTTP since a
			// browser would pin a host that may not serve HTTPS yet.
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if h.Get("Content-Security-Policy") == "" {
			h.Set("Content-Security-Policy", buildCSP())
		}
		next.ServeHTTP(w, r)
	})
}

// isSecure mirrors httpx.IsSecureRequest without importing it, keeping the
// middleware package free of a cycle.
func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
