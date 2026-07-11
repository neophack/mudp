package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
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

// DecodeJSON decodes a JSON request body into v.
func DecodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// RouteParam returns a path parameter from chi.
func RouteParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// RouteParamInt returns a path parameter parsed as int64.
func RouteParamInt(r *http.Request, name string) (int64, error) {
	s := RouteParam(r, name)
	if s == "" {
		return 0, fmt.Errorf("route parameter %q is required", name)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("route parameter %q must be an integer", name)
	}
	return v, nil
}

// QueryString returns a query parameter, or the fallback if missing/empty.
func QueryString(r *http.Request, name, fallback string) string {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return fallback
	}
	return v
}

// QueryInt returns a query parameter parsed as int, or the fallback if missing/invalid.
func QueryInt(r *http.Request, name string, fallback int) int {
	s := strings.TrimSpace(r.URL.Query().Get(name))
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

// QueryBool returns a query parameter parsed as bool.
func QueryBool(r *http.Request, name string, fallback bool) bool {
	s := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name)))
	switch s {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	case "":
		return fallback
	}
	return fallback
}

// RequireQuery returns a required query parameter.
func RequireQuery(r *http.Request, name string) (string, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return "", fmt.Errorf("query parameter %q is required", name)
	}
	return v, nil
}

// Method checks the request method and returns 405 if it does not match.
func Method(r *http.Request, allowed string) *HandlerError {
	if r.Method != allowed {
		return ErrorFromStatus(http.StatusMethodNotAllowed, fmt.Sprintf("method %s not allowed", r.Method))
	}
	return nil
}

// Validate is the interface payloads can implement for custom validation.
type Validate interface {
	Validate(*http.Request) error
}

// DecodeAndValidateJSON decodes and then validates a payload.
func DecodeAndValidateJSON(r *http.Request, v Validate) error {
	if err := DecodeJSON(r, v); err != nil {
		return err
	}
	if err := v.Validate(r); err != nil {
		return err
	}
	return nil
}
