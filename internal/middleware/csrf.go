package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"mudp/internal/httpx"
)

const csrfCookieName = "mudp_csrf"
const csrfHeaderName = "X-CSRF-Token"

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
		token := r.Header.Get(csrfHeaderName)
		if token == "" {
			token = r.URL.Query().Get("csrf_token")
		}
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
			httpx.WriteErr(w, &httpx.HandlerError{Status: http.StatusForbidden, Message: "CSRF token mismatch"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRFToken returns a new random CSRF token and sets the cookie.
func CSRFToken(w http.ResponseWriter, secure bool) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	cookie := &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
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
