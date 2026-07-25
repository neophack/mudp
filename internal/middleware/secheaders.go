package middleware

import (
	"net/http"
	"strings"
)

// appCSP is the policy for the console's own pages. The SPA loads every script
// from /vendor and /app.js on this origin, so 'self' is sufficient and no
// inline script allowance is needed. Styles keep 'unsafe-inline' because the UI
// sets element styles directly. frame-ancestors 'none' is the modern
// equivalent of X-Frame-Options and blocks clickjacking of the console.
const appCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' ws: wss:; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

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
			h.Set("Content-Security-Policy", appCSP)
		}
		next.ServeHTTP(w, r)
	})
}

// isSecure mirrors httpx.IsSecureRequest without importing it, keeping the
// middleware package free of a cycle.
func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
