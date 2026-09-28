package httpx

import (
	"context"
	"net/http"
	"strings"
)

type requestIDKey struct{}

// secureCheck is the trusted-proxy gate installed once at startup (see
// SetSecureCheck). When it rejects a request's peer, X-Forwarded-Proto is
// ignored — a direct client must not be able to forge its way to a Secure
// cookie flag or an HSTS pin.
var secureCheck func(*http.Request) bool

// SetSecureCheck installs the trusted-proxy gate consulted by
// IsSecureRequest. The server wires it to its configured proxy set so cookie
// flags use the same discipline as the HSTS middleware.
func SetSecureCheck(f func(*http.Request) bool) { secureCheck = f }

// IsSecureRequest reports whether the request was made over HTTPS, either
// directly (TLS on the connection) or via a trusted proxy that set
// X-Forwarded-Proto. It is used to decide the Secure flag for cookies.
func IsSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if secureCheck != nil && !secureCheck(r) {
		return false
	}
	return strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"
}

// WithRequestID stores a request ID in the request context.
func WithRequestID(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
}

// RequestID returns the request ID stored in the request context, if any.
func RequestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}
