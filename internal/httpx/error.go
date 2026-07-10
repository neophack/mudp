package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// HandlerError is the uniform error type returned by HTTP handlers.
// It mirrors Portainer's pkg/libhttp/error.HandlerError.
type HandlerError struct {
	Status  int
	Message string
	Err     error
}

func (e *HandlerError) Error() string {
	if e.Err != nil && e.Message != "" {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.Status)
}

func (e *HandlerError) Unwrap() error { return e.Err }

// Common constructors.
func BadRequest(message string, err ...error) *HandlerError {
	return newError(http.StatusBadRequest, message, err...)
}

func Unauthorized(message string, err ...error) *HandlerError {
	return newError(http.StatusUnauthorized, message, err...)
}

func Forbidden(message string, err ...error) *HandlerError {
	return newError(http.StatusForbidden, message, err...)
}

func NotFound(message string, err ...error) *HandlerError {
	return newError(http.StatusNotFound, message, err...)
}

func Conflict(message string, err ...error) *HandlerError {
	return newError(http.StatusConflict, message, err...)
}

func InternalServerError(message string, err ...error) *HandlerError {
	return newError(http.StatusInternalServerError, message, err...)
}

func newError(status int, message string, err ...error) *HandlerError {
	var underlying error
	if len(err) > 0 {
		underlying = err[0]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &HandlerError{Status: status, Message: message, Err: underlying}
}

// Handler is an HTTP handler that can return a structured error.
type Handler func(http.ResponseWriter, *http.Request) *HandlerError

// ServeHTTP wraps h so standard routers can use it. Errors are written as JSON.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteErr(w, err)
	}
}

// LoggerHandler is a chi-compatible adapter that logs returned errors.
func LoggerHandler(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			// The request-scoped logger is expected to be present via middleware.
			Logger(r).Error("handler failed", "status", err.Status, "error", err.Error())
			WriteErr(w, err)
		}
	}
}

// WriteErr writes a JSON error response. It always returns JSON, even for HEAD.
func WriteErr(w http.ResponseWriter, err *HandlerError) {
	WriteJSON(w, err.Status, map[string]any{"error": err.Message})
}

// ErrorFromStatus converts a status code and message into a HandlerError.
func ErrorFromStatus(status int, message string) *HandlerError {
	return &HandlerError{Status: status, Message: message}
}

// IsMethodNotAllowed reports whether err is an HTTP 405.
func IsMethodNotAllowed(err error) bool {
	var he *HandlerError
	return errors.As(err, &he) && he.Status == http.StatusMethodNotAllowed
}
