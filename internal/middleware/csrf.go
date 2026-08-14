package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"mudp/internal/auth"
	"mudp/internal/httpx"
)

const csrfCookieName = "mudp_csrf"
const csrfHeaderName = "X-CSRF-Token"

// CSRFTokenFromRequest returns the current CSRF token from the request cookie,
// or an empty string if none is present.
func CSRFTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// CSRFProtect requires a matching CSRF token cookie/header for state-changing
// requests. Safe methods (GET/HEAD/OPTIONS/TRACE) are exempt.
func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			httpx.WriteErr(w, &httpx.HandlerError{Status: http.StatusForbidden, Message: "CSRF token missing"})
			return
		}
		// Header-only (docs/SECURITY-AUDIT.md L-4): the shipped frontend always
		// sends X-CSRF-Token, and an URL fallback would leak the token into
		// access logs, proxy logs and browser history.
		token := r.Header.Get(csrfHeaderName)
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
			httpx.WriteErr(w, &httpx.HandlerError{Status: http.StatusForbidden, Message: "CSRF token mismatch"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRFToken returns a new random CSRF token and sets the cookie.
func CSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	cookie := &http.Cookie{
		Name:  csrfCookieName,
		Value: token,
		Path:  "/",
		// Pinned to the session cookie's lifetime. Without an explicit expiry
		// this is a browser-session cookie, so closing the browser dropped it
		// while the 24h session cookie survived — the user came back still
		// logged in, but every POST/DELETE failed with "CSRF token missing"
		// and nothing short of logging out and back in restored it.
		Expires:  time.Now().Add(auth.SessionTTL),
		MaxAge:   int(auth.SessionTTL / time.Second),
		HttpOnly: false,
		Secure:   httpx.IsSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, cookie)
	return token, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isSafeMethod(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}
