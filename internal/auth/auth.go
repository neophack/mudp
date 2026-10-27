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

// Signer issues and verifies HMAC-signed session cookies.
type Signer struct {
	secret []byte
}

// New creates a signer.
func New(secret string) Signer {
	return Signer{secret: []byte(secret)}
}

func (s Signer) Set(w http.ResponseWriter, r *http.Request, userID, epoch int64) {
	exp := time.Now().Add(SessionTTL).Unix()
	body := fmt.Sprintf("%d:%d:%d", userID, exp, epoch)
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
// user's current one: bumping a user's epoch (password change, admin reset)
// instantly invalidates every session already in the wild, so a leaked or
// remembered cookie cannot outlive the credential it was minted from.
func (s Signer) UserID(r *http.Request) (int64, int64, bool) {
	uid, epoch, _, ok := s.parse(r)
	return uid, epoch, ok
}

// Renew reissues the session cookie once less than half of SessionTTL is left,
// so an active client (e.g. a multi-hour upload sending request after request)
// never hits the fixed expiry mid-flight. It reports whether it renewed.
func (s Signer) Renew(w http.ResponseWriter, r *http.Request) bool {
	uid, epoch, exp, ok := s.parse(r)
	if !ok || time.Until(time.Unix(exp, 0)) > SessionTTL/2 {
		return false
	}
	s.Set(w, r, uid, epoch)
	return true
}

func (s Signer) parse(r *http.Request) (uid, epoch, exp int64, ok bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 4 {
		return
	}
	body := parts[0] + ":" + parts[1] + ":" + parts[2]
	if !hmac.Equal([]byte(parts[3]), []byte(s.sign(body))) {
		return
	}
	exp, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return
	}
	if uid, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return
	}
	if epoch, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
		return
	}
	return uid, epoch, exp, true
}

func (s Signer) sign(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
