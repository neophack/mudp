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

// Signer issues and verifies HMAC-signed session cookies.
type Signer struct {
	secret []byte
}

// New creates a signer.
func New(secret string) Signer {
	return Signer{secret: []byte(secret)}
}

func (s Signer) Set(w http.ResponseWriter, r *http.Request, userID int64) {
	exp := time.Now().Add(24 * time.Hour).Unix()
	body := fmt.Sprintf("%d:%d", userID, exp)
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

func (s Signer) UserID(r *http.Request) (int64, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 {
		return 0, false
	}
	body := parts[0] + ":" + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(s.sign(body))) {
		return 0, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, false
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	return uid, err == nil
}

func (s Signer) sign(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
