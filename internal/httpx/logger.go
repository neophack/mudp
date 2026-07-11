package httpx

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type loggerKey struct{}

// SLogger is a minimal structured logger interface used by httpx.
type SLogger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) SLogger
}

type defaultLogger struct {
	prefix string
}

func (l *defaultLogger) format(msg string, args []any) string {
	var b strings.Builder
	if l.prefix != "" {
		b.WriteString(l.prefix)
		if msg != "" {
			b.WriteString(" ")
		}
	}
	b.WriteString(msg)
	for i := 0; i+1 < len(args); i += 2 {
		b.WriteString(fmt.Sprintf(" %s=%v", args[i], args[i+1]))
	}
	return b.String()
}

func (l *defaultLogger) Debug(msg string, args ...any) {
	log.Println("[DEBUG] " + l.format(msg, args))
}

func (l *defaultLogger) Info(msg string, args ...any) {
	log.Println("[INFO] " + l.format(msg, args))
}

func (l *defaultLogger) Error(msg string, args ...any) {
	log.Println("[ERROR] " + l.format(msg, args))
}

func (l *defaultLogger) With(args ...any) SLogger {
	return &defaultLogger{prefix: l.format("", args)}
}

// DefaultLogger returns the default logger.
func DefaultLogger() SLogger {
	return &defaultLogger{}
}

// WithLogger stores a logger in the request context.
func WithLogger(r *http.Request, l SLogger) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), loggerKey{}, l))
}

// Logger returns the request-scoped logger, or the default logger.
func Logger(r *http.Request) SLogger {
	if l, ok := r.Context().Value(loggerKey{}).(SLogger); ok && l != nil {
		return l
	}
	return DefaultLogger()
}
