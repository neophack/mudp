package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mudp/internal/httpx"
)

const CookieName = "mudp_session"

// SessionTTL is how long a login lasts. The CSRF cookie is pinned to the same
// lifetime (see middleware.CSRFToken): if it expired first, a still-valid
// session would keep authenticating while every state-changing request was
// rejected for a missing CSRF token.
const SessionTTL = 24 * time.Hour

// MaxSessionAge is the absolute lifetime of a login. Renew slides the cookie
// expiry so active clients stay signed in, but the original issue time travels
// with every renewal and a cookie older than this is rejected regardless — a
// stolen cookie cannot be kept alive indefinitely by periodic use.
const MaxSessionAge = 7 * 24 * time.Hour

// Signer issues and verifies HMAC-signed session cookies.
type Signer struct {
	secret []byte
}

// New creates a signer.
func New(secret string) Signer {
	return Signer{secret: []byte(secret)}
}

// Set issues a fresh session cookie starting a new login (iat = now).
func (s Signer) Set(w http.ResponseWriter, r *http.Request, userID, epoch int64) {
	s.set(w, r, userID, epoch, time.Now().Unix())
}

// set writes the cookie with an explicit issue time. Renew passes the original
// iat so sliding the expiry never lengthens the login past MaxSessionAge.
func (s Signer) set(w http.ResponseWriter, r *http.Request, userID, epoch, iat int64) {
	exp := time.Now().Add(SessionTTL).Unix()
	body := fmt.Sprintf("%d:%d:%d:%d", userID, iat, exp, epoch)
	sig := s.sign(body)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(body + ":" + sig)),
		Path:     "/",
		Expires:  time.Unix(exp, 0),
		HttpOnly: true,
		Secure:   httpx.IsSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s Signer) Clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   httpx.IsSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// UserID verifies the session cookie and returns the user id plus the session
// epoch it was issued against. The caller compares the epoch against the
// user's current one: bumping a user's epoch (logout, password change, admin
// reset) instantly invalidates every session already in the wild, so a leaked
// or remembered cookie cannot outlive the credential it was minted from.
func (s Signer) UserID(r *http.Request) (int64, int64, bool) {
	uid, epoch, _, _, ok := s.parse(r)
	return uid, epoch, ok
}

// Renew reissues the session cookie once less than half of SessionTTL is left,
// so an active client (e.g. a multi-hour upload sending request after request)
// never hits the fixed expiry mid-flight. The original iat is carried over so
// the renewal cannot extend the login beyond MaxSessionAge. It reports whether
// it renewed.
func (s Signer) Renew(w http.ResponseWriter, r *http.Request) bool {
	uid, epoch, exp, iat, ok := s.parse(r)
	if !ok || time.Until(time.Unix(exp, 0)) > SessionTTL/2 {
		return false
	}
	s.set(w, r, uid, epoch, iat)
	return true
}

func (s Signer) parse(r *http.Request) (uid, epoch, exp, iat int64, ok bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 5 {
		return
	}
	body := parts[0] + ":" + parts[1] + ":" + parts[2] + ":" + parts[3]
	if !hmac.Equal([]byte(parts[4]), []byte(s.sign(body))) {
		return
	}
	if uid, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return
	}
	if iat, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return
	}
	if exp, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
		return
	}
	if epoch, err = strconv.ParseInt(parts[3], 10, 64); err != nil {
		return
	}
	now := time.Now().Unix()
	if now > exp || iat > now || now-iat > int64(MaxSessionAge/time.Second) {
		return
	}
	return uid, epoch, exp, iat, true
}

func (s Signer) sign(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
