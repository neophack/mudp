package httpx

import (
	"encoding/json"
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

func InternalServerError(message string, err ...error) *HandlerError {
	status := http.StatusInternalServerError
	var underlying error
	if len(err) > 0 {
		underlying = err[0]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &HandlerError{Status: status, Message: message, Err: underlying}
}

// WriteErr writes a JSON error response. It always returns JSON, even for HEAD.
func WriteErr(w http.ResponseWriter, err *HandlerError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(err.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Message})
}
