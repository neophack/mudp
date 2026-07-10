package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// WriteJSON writes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Last-ditch; cannot recover at this point.
		_, _ = fmt.Fprintf(w, `{"error":"failed to encode response"}`)
	}
}

// OK writes a 200 OK JSON response.
func OK(w http.ResponseWriter, v any) {
	WriteJSON(w, http.StatusOK, v)
}

// NoContent writes a 204 response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Respond is a helper for the common "data or error" pattern.
func Respond(w http.ResponseWriter, data any, err error) {
	if err != nil {
		WriteErr(w, InternalServerError("", err))
		return
	}
	OK(w, data)
}

// RespondOK writes {"ok": true} on nil error, otherwise an error JSON.
func RespondOK(w http.ResponseWriter, err error) {
	if err != nil {
		WriteErr(w, InternalServerError("", err))
		return
	}
	OK(w, map[string]bool{"ok": true})
}
