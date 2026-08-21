package httpx

import (
	"context"
	"net/http"
	"strings"
)

type requestIDKey struct{}

// IsSecureRequest reports whether the request was made over HTTPS, either
// directly (TLS on the connection) or via a trusted proxy that set
// X-Forwarded-Proto. It is used to decide the Secure flag for cookies.
func IsSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
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
