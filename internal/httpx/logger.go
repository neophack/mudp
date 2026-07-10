package httpx

import (
	"context"
	"log/slog"
	"net/http"
)

type loggerKey struct{}

// WithLogger stores a logger in the request context.
func WithLogger(r *http.Request, l *slog.Logger) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), loggerKey{}, l))
}

// Logger returns the request-scoped logger, or the default slog logger.
func Logger(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// RequestID returns the request ID from the context, if any.
func RequestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

type requestIDKey struct{}

// WithRequestID stores a request ID in the request context.
func WithRequestID(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
}
